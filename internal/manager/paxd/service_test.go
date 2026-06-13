package paxd_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
	paxdmocks "github.com/pax-beehive/pax-manager/internal/manager/paxd/mocks"
)

func TestRegisterAgent(t *testing.T) {
	t.Run(
		"Given a valid registration request without OS when registering then it defaults OS and stores a hashed API key",
		func(t *testing.T) {
			ctx := context.Background()
			meta := auth.NewRequestMetadata(map[string]string{"X-Registration-Token": "reg"})
			store := paxdmocks.NewMockStore(t)
			owners := paxdmocks.NewMockRegistrationOwnerResolver(t)
			secrets := paxdmocks.NewMockSecretIssuer(t)
			owner := domain.User{UserID: "usr_owner"}

			owners.EXPECT().RegistrationOwner(ctx, meta).Return(owner, nil).Once()
			secrets.EXPECT().New("pax").Return("pax_raw_key", nil).Once()
			secrets.EXPECT().Hash("pax_raw_key").Return("hashed_key").Once()
			store.EXPECT().
				RegisterAgent(ctx, owner, mock.MatchedBy(func(req domain.RegisterAgentRequest) bool {
					return req.Hostname == "host-a" && req.OS == "unknown"
				}), "hashed_key").
				Return(domain.Agent{AgentID: "agent_1"}, nil).
				Once()

			svc := paxd.NewService(store, fixedClock, owners, secrets)
			status, data, err := svc.RegisterAgent(
				ctx,
				meta,
				domain.RegisterAgentRequest{Hostname: "host-a"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, status)
			require.Equal(
				t,
				domain.RegisterAgentResponse{AgentID: "agent_1", APIKey: "pax_raw_key"},
				data,
			)
		},
	)

	t.Run(
		"Given a registration request without hostname when registering then it returns a bad request error",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.RegisterAgent(
				context.Background(),
				auth.RequestMetadata{},
				domain.RegisterAgentRequest{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)
}

func TestReportStatus(t *testing.T) {
	t.Run(
		"Given a status report without agent ID or timestamp when reporting then it fills both from the authenticated agent and clock",
		func(t *testing.T) {
			ctx := context.Background()
			store := paxdmocks.NewMockStore(t)
			store.EXPECT().
				UpsertAgentStatus(ctx, mock.MatchedBy(func(report domain.AgentStatusReport) bool {
					return report.AgentID == "agent_1" && report.Timestamp.Equal(fixedNow())
				})).
				Return(nil).
				Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ReportStatus(
				ctx,
				domain.Agent{AgentID: "agent_1"},
				domain.AgentStatusReport{},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, map[string]bool{"ok": true}, data)
		},
	)

	t.Run(
		"Given a status report for another agent when reporting then it returns forbidden",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.ReportStatus(
				context.Background(),
				domain.Agent{AgentID: "agent_1"},
				domain.AgentStatusReport{AgentID: "agent_2"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusForbidden, appErr.Status)
		},
	)
}

func TestMailboxOperations(t *testing.T) {
	t.Run(
		"Given no pull limit when pulling mailbox then it uses the default limit",
		func(t *testing.T) {
			ctx := context.Background()
			store := paxdmocks.NewMockStore(t)
			expected := domain.MailboxPull{MaxOffset: 10}
			store.EXPECT().
				PullMailbox(ctx, "agent_1", "", int64(3), 10).
				Return(expected, nil).
				Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.PullMailbox(ctx, domain.Agent{AgentID: "agent_1"}, 3, 0)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, expected, data)
		},
	)

	t.Run(
		"Given a session mailbox pull without limit then it filters by session and uses the default limit",
		func(t *testing.T) {
			ctx := context.Background()
			store := paxdmocks.NewMockStore(t)
			expected := domain.MailboxPull{MaxOffset: 11}
			store.EXPECT().
				PullMailbox(ctx, "agent_1", "sess_1", int64(4), 10).
				Return(expected, nil).
				Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.PullSessionMailbox(
				ctx,
				domain.Agent{AgentID: "agent_1"},
				"sess_1",
				4,
				0,
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, expected, data)
		},
	)

	t.Run(
		"Given a negative offset when updating mailbox offset then it rejects the request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.UpdateOffset(
				context.Background(),
				domain.Agent{AgentID: "agent_1"},
				-1,
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given a message result without status when reporting result then it rejects the request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.ReportMessageResult(
				context.Background(),
				domain.Agent{AgentID: "agent_1"},
				domain.MessageResultRequest{MessageID: "msg_1"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given storage fails when reporting result then it returns the storage error",
		func(t *testing.T) {
			ctx := context.Background()
			store := paxdmocks.NewMockStore(t)
			storeErr := errors.New("store failed")
			req := domain.MessageResultRequest{MessageID: "msg_1", Status: "completed"}
			store.EXPECT().MarkMessageResult(ctx, "agent_1", "msg_1", req).Return(storeErr).Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			_, _, err := svc.ReportMessageResult(ctx, domain.Agent{AgentID: "agent_1"}, req)

			require.ErrorIs(t, err, storeErr)
		},
	)
}

func fixedClock() time.Time {
	return fixedNow()
}

func fixedNow() time.Time {
	return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
}

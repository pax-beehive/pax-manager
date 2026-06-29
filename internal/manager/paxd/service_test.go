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
			require.Equal(t, http.StatusOK, status)
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

func TestReportNodeAgentSessions(t *testing.T) {
	t.Run(
		"Given a node-owned agent session report when reporting then it delegates session-only upsert",
		func(t *testing.T) {
			ctx := context.Background()
			node := domain.Node{NodeID: "node_1"}
			sessions := []domain.SessionStatusInput{{
				SessionID: "codex:abc",
				NativeID:  "abc",
				Status:    "available",
			}}
			store := paxdmocks.NewMockStore(t)
			store.EXPECT().UpsertAgentSessions(ctx, node, "agent_1", sessions).Return(nil).Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ReportNodeAgentSessions(
				ctx,
				node,
				"agent_1",
				domain.NodeAgentSessionReport{Sessions: sessions},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, map[string]bool{"ok": true}, data)
		},
	)

	t.Run(
		"Given missing agent ID when reporting sessions then it returns bad request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.ReportNodeAgentSessions(
				context.Background(),
				domain.Node{NodeID: "node_1"},
				"",
				domain.NodeAgentSessionReport{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given missing session ID when reporting sessions then it returns bad request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.ReportNodeAgentSessions(
				context.Background(),
				domain.Node{NodeID: "node_1"},
				"agent_1",
				domain.NodeAgentSessionReport{Sessions: []domain.SessionStatusInput{{}}},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given storage fails when reporting sessions then it returns the storage error",
		func(t *testing.T) {
			ctx := context.Background()
			node := domain.Node{NodeID: "node_1"}
			sessions := []domain.SessionStatusInput{{SessionID: "codex:abc"}}
			storeErr := errors.New("store failed")
			store := paxdmocks.NewMockStore(t)
			store.EXPECT().
				UpsertAgentSessions(ctx, node, "agent_1", sessions).
				Return(storeErr).
				Once()
			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.ReportNodeAgentSessions(
				ctx,
				node,
				"agent_1",
				domain.NodeAgentSessionReport{Sessions: sessions},
			)

			require.ErrorIs(t, err, storeErr)
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

func TestCreateNodeOutboundMessage(t *testing.T) {
	t.Run(
		"Given missing session ID when creating node outbound message then it returns bad request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.CreateNodeOutboundMessage(
				context.Background(),
				domain.Node{NodeID: "node_1"},
				domain.CreateOutboundMessageRequest{
					AgentID: "agent_1",
					Content: "done",
				},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)
}

func TestRegisterNodeAgent(t *testing.T) {
	t.Run(
		"Given both pax key and registration token when registering node agent then it rejects the request",
		func(t *testing.T) {
			meta := auth.NewRequestMetadata(map[string]string{
				"X-Pax-Key":            "pax_raw",
				"X-Registration-Token": "reg_raw",
			})
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.RegisterNodeAgent(
				context.Background(),
				meta,
				domain.RegisterNodeAgentRequest{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given no pax key or registration token when registering node agent then it returns unauthorized",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.RegisterNodeAgent(
				context.Background(),
				auth.RequestMetadata{},
				domain.RegisterNodeAgentRequest{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusUnauthorized, appErr.Status)
		},
	)

	t.Run(
		"Given a registration token when registering node agent then it registers the node and binds the agent to that node",
		func(t *testing.T) {
			ctx := context.Background()
			meta := auth.NewRequestMetadata(map[string]string{"X-Registration-Token": "reg_raw"})
			store := paxdmocks.NewMockStore(t)
			owners := paxdmocks.NewMockRegistrationOwnerResolver(t)
			secrets := paxdmocks.NewMockSecretIssuer(t)
			owner := domain.User{UserID: "usr_owner"}
			node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_owner"}
			agent := domain.Agent{AgentID: "agent_1", NodeID: "node_1", OwnerUserID: "usr_owner"}

			owners.EXPECT().RegistrationOwner(ctx, meta).Return(owner, nil).Once()
			secrets.EXPECT().New("pax").Return("pax_raw_key", nil).Once()
			secrets.EXPECT().Hash("pax_raw_key").Return("hashed_key").Once()
			store.EXPECT().
				RegisterNode(ctx, owner, mock.MatchedBy(func(req domain.RegisterNodeRequest) bool {
					return req.Hostname == "node-host" && req.OS == "unknown"
				}), "hashed_key").
				Return(node, nil).
				Once()
			store.EXPECT().
				CreateNodeAgent(
					ctx,
					domain.UserPrincipal{User: domain.User{UserID: "usr_owner"}},
					mock.MatchedBy(func(req domain.CreateAgentRequest) bool {
						return req.NodeID == "node_1" && req.Name == "codex"
					}),
				).
				Return(agent, domain.MailboxMessage{}, nil).
				Once()

			svc := paxd.NewService(store, fixedClock, owners, secrets)
			status, data, err := svc.RegisterNodeAgent(
				ctx,
				meta,
				domain.RegisterNodeAgentRequest{
					Node:  domain.RegisterNodeRequest{Hostname: "node-host"},
					Agent: domain.CreateAgentRequest{Name: "codex"},
				},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			resp := data.(domain.RegisterNodeAgentResponse)
			require.Equal(t, "node_1", resp.NodeID)
			require.Equal(t, "pax_raw_key", resp.APIKey)
			require.Equal(t, "agent_1", resp.AgentID)
		},
	)
}

func TestNodeMailboxOperations(t *testing.T) {
	t.Run(
		"Given no pull limit when pulling node mailbox then it uses the default limit",
		func(t *testing.T) {
			ctx := context.Background()
			store := paxdmocks.NewMockStore(t)
			expected := domain.MailboxPull{MaxOffset: 12}
			store.EXPECT().
				PullNodeMailbox(ctx, "node_1", "agent_1", "sess_1", int64(2), 10).
				Return(expected, nil).
				Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.PullNodeMailbox(
				ctx,
				domain.Node{NodeID: "node_1"},
				"agent_1",
				"sess_1",
				2,
				0,
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, expected, data)
		},
	)

	t.Run(
		"Given a negative node offset when updating offset then it rejects the request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.UpdateNodeOffset(
				context.Background(),
				domain.Node{NodeID: "node_1"},
				-1,
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)

	t.Run(
		"Given node message result without status then it defaults to completed",
		func(t *testing.T) {
			ctx := context.Background()
			store := paxdmocks.NewMockStore(t)
			store.EXPECT().
				MarkNodeMessageResult(
					ctx,
					"node_1",
					"msg_1",
					mock.MatchedBy(func(req domain.MessageResultRequest) bool {
						return req.MessageID == "msg_1" && req.Status == "completed"
					}),
				).
				Return(nil).
				Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ReportNodeMessageResult(
				ctx,
				domain.Node{NodeID: "node_1"},
				domain.MessageResultRequest{MessageID: "msg_1"},
			)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, map[string]bool{"ok": true}, data)
		},
	)

	t.Run(
		"Given delivered marker without message ID then it rejects the request",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.MarkNodeMessageDelivered(
				context.Background(),
				domain.Node{NodeID: "node_1"},
				domain.MarkDeliveredRequest{},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusBadRequest, appErr.Status)
		},
	)
}

func TestReportNodeStatus(t *testing.T) {
	t.Run(
		"Given node status without node ID or timestamp then it fills both from token and clock",
		func(t *testing.T) {
			ctx := context.Background()
			node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_owner"}
			store := paxdmocks.NewMockStore(t)
			store.EXPECT().
				UpsertNodeStatus(ctx, node, mock.MatchedBy(func(report domain.NodeStatusReport) bool {
					return report.NodeID == "node_1" && report.Timestamp.Equal(fixedNow())
				})).
				Return(nil).
				Once()

			svc := paxd.NewService(
				store,
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)
			status, data, err := svc.ReportNodeStatus(ctx, node, domain.NodeStatusReport{})

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, map[string]bool{"ok": true}, data)
		},
	)

	t.Run(
		"Given a node status for another node then it returns forbidden",
		func(t *testing.T) {
			svc := paxd.NewService(
				paxdmocks.NewMockStore(t),
				fixedClock,
				paxdmocks.NewMockRegistrationOwnerResolver(t),
				paxdmocks.NewMockSecretIssuer(t),
			)

			_, _, err := svc.ReportNodeStatus(
				context.Background(),
				domain.Node{NodeID: "node_1"},
				domain.NodeStatusReport{NodeID: "node_2"},
			)

			var appErr apperr.Error
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, http.StatusForbidden, appErr.Status)
		},
	)
}

func TestSecretRequestValidation(t *testing.T) {
	svc := paxd.NewService(
		paxdmocks.NewMockStore(t),
		fixedClock,
		paxdmocks.NewMockRegistrationOwnerResolver(t),
		paxdmocks.NewMockSecretIssuer(t),
	)
	_, _, err := svc.ResolveSecret(
		context.Background(),
		domain.Node{NodeID: "node_1"},
		domain.ResolveSecretRequest{SecretID: "secret_1"},
	)
	var appErr apperr.Error
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusBadRequest, appErr.Status)

	_, _, err = svc.WriteSecretVersion(
		context.Background(),
		domain.Node{NodeID: "node_1"},
		domain.WriteSecretVersionRequest{
			SecretID: "secret_1",
			AgentID:  "agent_1",
		},
	)
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusBadRequest, appErr.Status)
}

func fixedClock() time.Time {
	return fixedNow()
}

func fixedNow() time.Time {
	return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
}

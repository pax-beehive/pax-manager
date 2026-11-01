package userapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

func TestCanonicalSessionDetail(t *testing.T) {
	ctx := context.Background()
	principal := userPrincipal("owner", false)
	failure := errors.New("storage unavailable")
	for _, tc := range []struct {
		name, id     string
		principalErr error
		sessionErr   error
		agentErr     error
		headErr      error
		legacy       bool
		wantErr      error
	}{
		{name: "Given no identity then no storage is read", id: "session", principalErr: failure, wantErr: failure},
		{name: "Given no canonical ID then reject before storage", wantErr: apperr.Error{Status: http.StatusBadRequest, Message: "session_id is required"}},
		{name: "Given an inaccessible or absent session then preserve not found", id: "session", sessionErr: domain.ErrNotFound, wantErr: domain.ErrNotFound},
		{name: "Given a failed metadata read then preserve its error", id: "session", sessionErr: failure, wantErr: failure},
		{name: "Given known context then return it with one history head read", id: "session"},
		{name: "Given a legacy session then resolve its node through its authorized agent", id: "session", legacy: true},
		{name: "Given an inaccessible legacy agent then hide the session", id: "session", legacy: true, agentErr: domain.ErrNotFound, wantErr: domain.ErrNotFound},
		{name: "Given a failed head read then return an error rather than incomplete metadata", id: "session", headErr: failure, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := userapimocks.NewMockStore(t)
			principals := userapimocks.NewMockPrincipalResolver(t)
			principals.EXPECT().
				Principal(ctx, auth.RequestMetadata{}).
				Return(principal, tc.principalErr).
				Once()
			session := domain.AgentSession{SessionID: "session", AgentID: "agent", NodeID: "node"}
			if tc.legacy {
				session.NodeID = ""
			}
			if tc.principalErr == nil && tc.id != "" {
				store.EXPECT().
					GetSession(ctx, principal, tc.id).
					Return(session, tc.sessionErr).
					Once()
				if tc.sessionErr == nil && tc.legacy {
					store.EXPECT().
						GetAgent(ctx, principal, "agent").
						Return(domain.Agent{AgentID: "agent", NodeID: "node"}, tc.agentErr).
						Once()
				}
			}
			withHead := &sessionDetailHeadStore{MockStore: store, headErr: tc.headErr}
			svc := userapi.NewService(
				withHead,
				fixedUserClock,
				principals,
				userapimocks.NewMockSecretIssuer(t),
			)
			status, result, err := svc.GetUserSession(ctx, auth.RequestMetadata{}, tc.id)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			session.NodeID = "node"
			session.LatestMessageID = "message"
			session.LatestMessageSeq = 7
			session.LatestTurnID = "turn"
			assert.Equal(t, session, result)
			assert.Equal(t, 1, withHead.calls)
		})
	}
}

type sessionDetailHeadStore struct {
	*userapimocks.MockStore
	calls   int
	headErr error
}

func (s *sessionDetailHeadStore) LatestSessionMessage(
	_ context.Context,
	agentID, sessionID string,
) (domain.Message, error) {
	s.calls++
	return domain.Message{
		MessageID:  "message",
		AgentID:    agentID,
		SessionID:  sessionID,
		SessionSeq: 7,
		TurnID:     "turn",
	}, s.headErr
}

func (*sessionDetailHeadStore) ListTurnSummaryPage(
	context.Context,
	string,
	string,
	string,
	int64,
	int64,
	int,
) (domain.MessageHistoryPage, error) {
	panic("canonical metadata must not page through history")
}

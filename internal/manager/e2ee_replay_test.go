package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestE2EEReplayGivenAuthenticatedOwnerThenReturnsOnlyOpaqueLatestTurn(t *testing.T) {
	s, _ := testServer(t, "replay@example.com")
	f := testNodeAgent(t, s, "replay@example.com")
	createConversationTestSession(t, s, f, "session", "native")
	p := testUserPrincipal(t, s, f.userEmail)
	for _, turn := range []string{"old", "new"} {
		_, _, err := s.store.InsertAgentEvent(
			t.Context(),
			domain.AgentEvent{
				TurnRef: turn,
				E2EERecord: domain.E2EERecord{
					RecordID:        turn,
					OwnerUserID:     p.User.UserID,
					AgentID:         f.agentID,
					SessionID:       "session",
					Kind:            "acp_event",
					ProtocolVersion: 1,
					CipherVersion:   1,
					KeyEpoch:        1,
					Nonce:           []byte("123456789012"),
					Ciphertext:      []byte("opaque encrypted bytes"),
					CreatedAt:       time.Now(),
				},
			},
		)
		require.NoError(t, err)
	}
	path := "/api/v1/user/self/agents/" + f.agentID + "/sessions/session/encrypted-events?view=replay"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-User-Email", f.userEmail)
	rec := httptest.NewRecorder()
	s.handleE2EEEvents(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var response struct {
		Data struct {
			TurnRef  string            `json:"turn_ref"`
			Events   []json.RawMessage `json:"events"`
			HasOlder bool              `json:"has_older"`
		}
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, "new", response.Data.TurnRef)
	assert.Len(t, response.Data.Events, 1)
	assert.True(t, response.Data.HasOlder)
	assert.NotContains(t, rec.Body.String(), "opaque encrypted bytes")
	for _, suffix := range []string{"&limit=0", "&limit=501", "&limit=x", "&after_cursor=-1", "&through_cursor=9007199254740992", "&before_turn=1&through_cursor=2", "&turn_ref=bad%20ref", "&turn_ref=turn", "&after_cursor=3&through_cursor=2"} {
		req := httptest.NewRequest(http.MethodGet, path+suffix, nil)
		req.Header.Set("X-User-Email", f.userEmail)
		rec := httptest.NewRecorder()
		s.handleE2EEEvents(rec, req)
		assert.Equal(t, 400, rec.Code, suffix)
	}
	req = httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-User-Email", "other@example.com")
	rec = httptest.NewRecorder()
	s.handleE2EEEvents(rec, req)
	assert.NotEqual(t, 200, rec.Code)
}

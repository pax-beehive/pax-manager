package manager

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSessionHistorySyncHeadAndBoundedTurnPages(t *testing.T) {
	srv, _ := testServer(t, "history-sync@example.com")
	fixture := testNodeAgent(t, srv, "history-sync@example.com")
	createConversationTestSession(t, srv, fixture, "sync-session", "native-sync")
	for i, turn := range []string{"older", "target", "target", "target", "later"} {
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &domain.Message{
			MessageID: fmt.Sprintf(
				"m%d",
				i,
			), AgentID: fixture.agentID, SessionID: "sync-session", TurnID: turn,
			Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
			Role: "assistant", MessageType: "agent_message_chunk",
		}))
	}
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-Email", fixture.userEmail)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}
	rec := get(
		"/api/v1/user/self/nodes/" + fixture.nodeID + "/agents/" + fixture.agentID + "/sessions/sync-session",
	)
	session := decodeData[domain.AgentSession](t, rec.Body.Bytes())
	require.Equal(t, "m4", session.LatestMessageID)
	require.Equal(t, int64(5), session.LatestMessageSeq)
	require.Equal(t, "later", session.LatestTurnID)
	type historyPage struct {
		Messages   []domain.MessageWithParts       `json:"messages"`
		Pagination domain.MessageHistoryPagination `json:"pagination"`
	}
	path := "/api/v1/user/self/sessions/sync-session/history?view=summary&turn_id=target&limit=2"
	latest := decodeData[historyPage](t, get(path).Body.Bytes())
	require.Len(t, latest.Messages, 2)
	require.True(t, latest.Pagination.HasOlder)
	require.Equal(t, "m2", latest.Messages[0].MessageID)
	require.Equal(t, "m3", latest.Messages[1].MessageID)
	older := decodeData[historyPage](
		t,
		get(fmt.Sprintf("%s&before_seq=%d", path, latest.Pagination.NextBeforeSeq)).Body.Bytes(),
	)
	require.Len(t, older.Messages, 1)
	require.Equal(t, "m1", older.Messages[0].MessageID)
	require.False(t, older.Pagination.HasOlder)
	forward := decodeData[historyPage](t, get(path+"&after_seq=2").Body.Bytes())
	require.Len(t, forward.Messages, 2)
	require.Equal(t, "m2", forward.Messages[0].MessageID)
}

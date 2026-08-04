package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type conversationTurnInspectingWriter struct {
	header  http.Header
	body    bytes.Buffer
	onWrite func([]byte)
}

func (w *conversationTurnInspectingWriter) Header() http.Header {
	return w.header
}

func (w *conversationTurnInspectingWriter) Write(payload []byte) (int, error) {
	if w.onWrite != nil {
		w.onWrite(payload)
	}
	return w.body.Write(payload)
}

func (w *conversationTurnInspectingWriter) WriteHeader(_ int) {}

func (w *conversationTurnInspectingWriter) Flush() {}

func TestConversationTurnDoneGivenPendingHistoryTextWhenEmittedThenFlushesAndPersistsCompletionFirst(
	t *testing.T,
) {
	ctx := t.Context()
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	createConversationTestSession(t, srv, fixture, "sess-turn", "native-turn")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	agent := &ACPTunnelAgent{
		agentID:     fixture.agentID,
		ownerUserID: principal.User.UserID,
		nodeID:      fixture.nodeID,
		store:       srv.store,
	}
	state := agent.liveState()
	state.mu.Lock()
	state.historyTextBatcher = newACPHistoryTextBatcher(srv.store, time.Hour, 1<<20)
	state.mu.Unlock()

	message := domain.Message{
		MessageID:   "msg-live-text",
		OwnerUserID: agent.ownerUserID,
		NodeID:      agent.nodeID,
		AgentID:     agent.agentID,
		SessionID:   "sess-turn",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		Status:      "received",
		MessageType: "agent_message_chunk",
		TurnID:      "turn_business",
		LogicalKey:  "test:live-text",
	}
	require.NoError(t, srv.store.UpsertMessage(ctx, &message))
	require.NoError(t, agent.appendHistoryText(ctx, message.MessageID, 0, "durable text"))
	parts, err := srv.store.ListMessageParts(ctx, message.MessageID)
	require.NoError(t, err)
	assert.Empty(t, parts)

	inspected := false
	w := &conversationTurnInspectingWriter{header: make(http.Header)}
	w.onWrite = func(payload []byte) {
		if !bytes.Contains(payload, []byte(`"type":"turn_done"`)) {
			return
		}
		inspected = true
		assert.NotContains(t, w.body.String(), `"type":"turn_done"`)
		flushedParts, listErr := srv.store.ListMessageParts(ctx, message.MessageID)
		require.NoError(t, listErr)
		require.Len(t, flushedParts, 1)
		assert.Equal(t, "durable text", flushedParts[0].Text)

		historyReq := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/agents/"+fixture.agentID+"/sessions/sess-turn/history",
			nil,
		)
		historyReq.Header.Set("X-User-Email", fixture.userEmail)
		historyRec := httptest.NewRecorder()
		srv.routes().ServeHTTP(historyRec, historyReq)
		require.Equal(t, http.StatusOK, historyRec.Code, historyRec.Body.String())
		history := decodeData[struct {
			Messages []MessageWithParts `json:"messages"`
		}](t, historyRec.Body.Bytes())
		completion := requireHistoryMessageWithPartsType(t, history.Messages, "turn_done")
		assert.Equal(t, "turn_business", completion.TurnID)
		assert.Equal(t, "complete", completion.Status)
		var marker struct {
			TurnStatus string `json:"turn_status"`
		}
		require.NoError(t, json.Unmarshal(completion.RawJSON, &marker))
		assert.Equal(t, "complete", marker.TurnStatus)
	}

	require.NoError(t, srv.writeConversationTurnDone(
		ctx,
		w,
		w,
		agent,
		conversationSession{managerID: "sess-turn"},
		"turn_business",
	))
	assert.True(t, inspected)
}

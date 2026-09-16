package manager

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func parseSSEEvents(t *testing.T, body string) []conversationEvent {
	t.Helper()
	events := make([]conversationEvent, 0)
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			continue
		}
		var ev conversationEvent
		require.NoError(t, json.Unmarshal([]byte(data), &ev))
		events = append(events, ev)
	}
	return events
}

func TestTurnTranscriptScopeAndMutableMessages(t *testing.T) {
	store := storage.NewMemoryStore(time.Now)
	ctx := t.Context()
	for i := 1; i <= 205; i++ {
		id := fmt.Sprintf("m%d", i)
		require.NoError(t, store.UpsertMessage(ctx, &domain.Message{
			Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
			MessageID: id, AgentID: "a", SessionID: "s", TurnID: "turn_1", Role: "assistant",
		}))
		require.NoError(t, store.UpsertMessagePart(ctx, &domain.MessagePart{
			MessageID: id, PartType: domain.MessagePartText, Text: "A",
		}))
	}
	for i, scope := range [][3]string{{"a", "s", "turn_other"}, {"a", "other", "turn_1"}, {"other", "s", "turn_1"}} {
		require.NoError(t, store.UpsertMessage(ctx, &domain.Message{
			Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
			MessageID: fmt.Sprintf(
				"excluded%d",
				i,
			), AgentID: scope[0], SessionID: scope[1], TurnID: scope[2],
		}))
	}
	items, err := store.ListTurnTranscript(ctx, "a", "s", "turn_1")
	require.NoError(t, err)
	require.Len(t, items, 205)
	rec := httptest.NewRecorder()
	base := conversationEvent{AgentID: "a", SessionID: "s", TurnID: "turn_1"}
	versions := map[string]string{}
	changed, complete, err := writeTurnTranscriptUpdate(rec, rec, base, items, versions)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, complete)
	events := parseSSEEvents(t, rec.Body.String())
	require.Len(t, events, 206)
	assert.Equal(t, "head", events[205].Type)
	assert.NotContains(t, rec.Body.String(), "excluded")
	originalSeq := items[0].SessionSeq
	require.NoError(t, store.UpsertMessagePart(ctx, &domain.MessagePart{
		MessageID: "m1", PartType: domain.MessagePartText, Text: "AB",
	}))
	items, err = store.ListTurnTranscript(ctx, "a", "s", "turn_1")
	require.NoError(t, err)
	require.Equal(t, originalSeq, items[0].SessionSeq)
	rec = httptest.NewRecorder()
	changed, complete, err = writeTurnTranscriptUpdate(rec, rec, base, items, versions)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, complete)
	events = parseSSEEvents(t, rec.Body.String())
	require.Len(t, events, 2)
	assert.Equal(t, "m1", events[0].MessageID)
	assert.Equal(t, "AB", events[0].Item.Parts[0].Text)
}

func TestSessionObserverCursorContract(t *testing.T) {
	for _, tc := range []struct {
		query, header string
		want          int64
		invalid       bool
	}{
		{"?after_seq=7", "12", 7, false}, {"", "12", 12, false}, {"", "", 0, false},
		{"?after_seq=-1", "", 0, true}, {"?after_seq=oops", "", 0, true},
		{"?after_message_id=m1", "", 0, true}, {"", "abc", 0, true},
	} {
		t.Run(tc.query+tc.header, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/events"+tc.query, nil)
			req.Header.Set("Last-Event-ID", tc.header)
			got, err := parseSessionObserverCursor(req)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSessionObserverExplicitTurnReplayAndValidation(t *testing.T) {
	srv, _ := testServer(t, "observer@example.com")
	fixture := testNodeAgent(t, srv, "observer@example.com")
	createConversationTestSession(
		t,
		srv,
		fixture,
		"sess-observer-contract",
		"native-observer-contract",
	)
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &domain.Message{
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		MessageID: "finished", AgentID: fixture.agentID, SessionID: "sess-observer-contract",
		TurnID: "turn_finished", MessageType: "turn_done",
	}))
	for _, tc := range []struct {
		query              string
		status             int
		includes, excludes string
	}{
		{"", 200, "no_running_turn", "history_item"},
		{"?turn_id=turn_finished", 200, "turn_done", "no_running_turn"},
		{"?turn_id=turn_finished&after_seq=1", 200, "history_item", "no_running_turn"},
		{"?turn_id=turn_finished&after_seq=99", 409, "cursor", "history_item"},
		{"?turn_id=turn_missing", 404, "turn not found", "history_item"},
		{"?after_seq=1", 400, "turn_id is required", "history_item"},
		{"?after_message_id=finished", 400, "unsupported", "history_item"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/user/self/agents/"+fixture.agentID+
				"/sessions/sess-observer-contract/events"+tc.query, nil)
			req.Header.Set("X-User-Email", fixture.userEmail)
			rec := httptest.NewRecorder()
			srv.handleSessionObserverEvents(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.includes)
			assert.NotContains(t, rec.Body.String(), tc.excludes)
			if tc.status == 200 && tc.query != "" {
				events := parseSSEEvents(t, rec.Body.String())
				require.Len(t, events, 4)
				assert.Equal(t, "turn_start", events[0].Type)
				assert.Equal(t, "head", events[2].Type)
				for _, event := range events {
					assert.Equal(t, "turn_finished", event.TurnID)
				}
			}
		})
	}
}

func TestTurnTranscriptRemovesSupersededDisplayRows(t *testing.T) {
	base := conversationEvent{AgentID: "a", SessionID: "s", TurnID: "turn_1"}
	versions := map[string]string{}
	rec := httptest.NewRecorder()
	items := []domain.MessageWithParts{{Message: domain.Message{
		MessageID: "raw", SessionID: "s", TurnID: "turn_1", SessionSeq: 1,
	}}}
	_, _, err := writeTurnTranscriptUpdate(rec, rec, base, items, versions)
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	changed, _, err := writeTurnTranscriptUpdate(rec, rec, base, nil, versions)
	require.NoError(t, err)
	require.True(t, changed)
	events := parseSSEEvents(t, rec.Body.String())
	require.Len(t, events, 2)
	assert.Equal(t, "history_remove", events[0].Type)
	assert.Equal(t, "raw", events[0].MessageID)
	assert.Empty(t, versions)
}

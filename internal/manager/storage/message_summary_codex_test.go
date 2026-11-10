package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestToolSummaryGivenCodexActivityThenKeepsValidatedLifecycleMetadata(t *testing.T) {
	for _, kind := range []string{"started", "interacted", "completed", "interrupted"} {
		t.Run(kind, func(t *testing.T) {
			for _, key := range []string{"rawInput", "raw_input"} {
				t.Run(key, func(t *testing.T) {
					update := map[string]any{
						"toolCallId": "call", "status": "completed",
						key: map[string]any{
							"activityKind": " \t" + kind + "\n", "agentThreadId": " child \r",
							"agentPath": " /root/research ", "prompt": "private large payload",
						},
					}
					raw, err := json.Marshal(toolSummaryFromUpdate(update))
					require.NoError(t, err)
					assert.JSONEq(t, `{"tool_call_id":"call","title":"","status":"completed",
						"codex_subagent":{"thread_id":"child","activity_kind":"`+kind+`",
						"agent_path":"/root/research"}}`, string(raw))
				})
			}
		})
	}
}

func TestToolSummaryGivenInvalidCodexActivityThenOmitsLifecycleMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
	}{
		{name: "absent input"},
		{name: "non-object input", input: "started"},
		{name: "absent kind", input: map[string]any{"agentThreadId": "child"}},
		{name: "unknown kind", input: map[string]any{
			"activityKind": "running", "agentThreadId": "child",
		}},
		{name: "non-string kind", input: map[string]any{
			"activityKind": 1, "agentThreadId": "child",
		}},
		{name: "absent thread", input: map[string]any{"activityKind": "started"}},
		{name: "blank thread", input: map[string]any{
			"activityKind": "started", "agentThreadId": " \t\n",
		}},
		{name: "non-string thread", input: map[string]any{
			"activityKind": "started", "agentThreadId": 42,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(toolSummaryFromUpdate(map[string]any{"rawInput": tc.input}))
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "codex_subagent")
		})
	}
}

func TestToolSummaryGivenCodexInputAliasesThenPrefersObjectAndOmitsInvalidOptionalPath(
	t *testing.T,
) {
	for _, path := range []any{nil, " \t\n", 123} {
		update := map[string]any{
			"rawInput": nil,
			"raw_input": map[string]any{
				"activityKind": "completed", "agentThreadId": "child", "agentPath": path,
			},
		}
		raw, err := json.Marshal(toolSummaryFromUpdate(update))
		require.NoError(t, err)
		assert.JSONEq(t, `{"tool_call_id":"","title":"","status":"",
			"codex_subagent":{"thread_id":"child","activity_kind":"completed"}}`, string(raw))
	}
	update := map[string]any{
		"rawInput":  map[string]any{"activityKind": "started", "agentThreadId": "first"},
		"raw_input": map[string]any{"activityKind": "completed", "agentThreadId": "second"},
	}
	raw, err := json.Marshal(toolSummaryFromUpdate(update))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"thread_id":"first"`)
}

func TestMessageSummaryGivenCodexUpdateThenHistoryAndTranscriptRetainLifecycle(t *testing.T) {
	store := NewMemoryStore(time.Now)
	message := Message{
		MessageID: "activity", AgentID: "agent", SessionID: "session", TurnID: "turn",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
		MessageType: "tool_call_update", RawJSON: json.RawMessage(`{"params":{"update":{
			"toolCallId":"call","status":"completed",
			"rawInput":{"activityKind":"started","agentThreadId":"child","agentPath":"/root/child"}
		}}}`),
	}
	require.NoError(t, store.UpsertMessage(t.Context(), &message))
	page, err := store.ListMessageSummaryPage(t.Context(), "agent", "session", 0, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	assert.JSONEq(t, `{"tool_call_id":"call","title":"","status":"completed",
		"codex_subagent":{"thread_id":"child","activity_kind":"started","agent_path":"/root/child"}}`,
		string(page.Messages[0].RawJSON))
	transcript, err := store.ListTurnTranscript(t.Context(), "agent", "session", "turn")
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	raw, err := json.Marshal(transcript[0].Tool)
	require.NoError(t, err)
	assert.JSONEq(t, string(page.Messages[0].RawJSON), string(raw))
}

package manager

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeACPToolCallFrames deep-merges a later ACP tool_call_update frame (patch)
// into the accumulated frame (base) using "set-if-present" semantics, mirroring
// mergeRuntimeToolCall: a field is overwritten only when the patch carries a
// meaningful value, so partial updates never clobber fields introduced by an
// earlier frame. These BDD-style cases pin that contract down before the
// history projector relies on it.
func TestMergeACPToolCallFrames(t *testing.T) {
	t.Run("given empty base then returns the patch verbatim", func(t *testing.T) {
		patch := json.RawMessage(`{"params":{"update":{"toolCallId":"tc-1","status":"pending"}}}`)
		got := mergeACPToolCallFrames(nil, patch)
		assert.JSONEq(t, string(patch), string(got))
	})

	t.Run("given empty patch then keeps the base verbatim", func(t *testing.T) {
		base := json.RawMessage(`{"params":{"update":{"toolCallId":"tc-1","title":"Read"}}}`)
		got := mergeACPToolCallFrames(base, nil)
		assert.JSONEq(t, string(base), string(got))
	})

	t.Run("given a status-only patch then earlier title and input survive", func(t *testing.T) {
		base := json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Read file","kind":"read","rawInput":{"path":"a.go"}}}}`,
		)
		patch := json.RawMessage(
			`{"method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}}}`,
		)

		got := mergeACPToolCallFrames(base, patch)

		update := updateObject(t, got)
		assert.Equal(t, "Read file", update["title"], "title from first frame must survive")
		assert.Equal(t, "read", update["kind"], "kind from first frame must survive")
		assert.Equal(t, "completed", update["status"], "status must advance to the latest")
		assert.Equal(t, "tool_call_update", update["sessionUpdate"], "sessionUpdate advances")
		require.IsType(t, map[string]any{}, update["rawInput"])
		assert.Equal(t, "a.go", update["rawInput"].(map[string]any)["path"], "rawInput must survive")
	})

	t.Run("given a patch with a fresh output then output is replaced with the latest", func(t *testing.T) {
		base := json.RawMessage(
			`{"params":{"update":{"toolCallId":"tc-1","content":[{"type":"text","text":"partial"}]}}}`,
		)
		patch := json.RawMessage(
			`{"params":{"update":{"toolCallId":"tc-1","content":[{"type":"text","text":"final"}]}}}`,
		)

		got := mergeACPToolCallFrames(base, patch)

		update := updateObject(t, got)
		content, ok := update["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		assert.Equal(t, "final", content[0].(map[string]any)["text"], "latest output replaces prior")
	})

	t.Run("given a patch with an empty-string field then the base value is preserved", func(t *testing.T) {
		base := json.RawMessage(`{"params":{"update":{"toolCallId":"tc-1","title":"Read file"}}}`)
		patch := json.RawMessage(`{"params":{"update":{"toolCallId":"tc-1","title":"","status":"failed"}}}`)

		got := mergeACPToolCallFrames(base, patch)

		update := updateObject(t, got)
		assert.Equal(t, "Read file", update["title"], "empty patch string must not clobber")
		assert.Equal(t, "failed", update["status"])
	})

	t.Run("given the same patch applied twice then the result is idempotent", func(t *testing.T) {
		base := json.RawMessage(
			`{"params":{"update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Read","kind":"read"}}}`,
		)
		patch := json.RawMessage(
			`{"params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}}}`,
		)

		once := mergeACPToolCallFrames(base, patch)
		twice := mergeACPToolCallFrames(once, patch)

		assert.JSONEq(t, string(once), string(twice), "replaying the same frame must not change state")
	})

	t.Run("given large integer fields then formatting is preserved without exponents", func(t *testing.T) {
		base := json.RawMessage(`{"params":{"update":{"toolCallId":"tc-1"}}}`)
		patch := json.RawMessage(`{"id":6,"params":{"update":{"toolCallId":"tc-1","bytes":79974123456}}}`)

		got := mergeACPToolCallFrames(base, patch)

		assert.Contains(t, string(got), "79974123456", "integers must not be reformatted as float exponents")
	})
}

func TestMergeACPToolCallFramesFallsBackOnInvalidJSON(t *testing.T) {
	valid := json.RawMessage(`{"params":{"update":{"toolCallId":"tc-1","status":"completed"}}}`)

	t.Run("given an invalid base then the patch wins", func(t *testing.T) {
		got := mergeACPToolCallFrames(json.RawMessage(`{not json`), valid)
		assert.JSONEq(t, string(valid), string(got))
	})

	t.Run("given an invalid patch then the base is kept", func(t *testing.T) {
		got := mergeACPToolCallFrames(valid, json.RawMessage(`{not json`))
		assert.JSONEq(t, string(valid), string(got))
	})
}

func TestACPHistoryIsMergeableToolCall(t *testing.T) {
	cases := []struct {
		name   string
		fields acpHistoryFields
		want   bool
	}{
		{"tool_call with id", acpHistoryFields{SessionUpdate: "tool_call", ToolCallID: "tc-1"}, true},
		{"tool_call_update with id", acpHistoryFields{SessionUpdate: "tool_call_update", ToolCallID: "tc-1"}, true},
		{"terminal delta is excluded", acpHistoryFields{SessionUpdate: "tool_call_update", ToolCallID: "tc-1", TerminalID: "t-1"}, false},
		{"missing toolCallId", acpHistoryFields{SessionUpdate: "tool_call"}, false},
		{"unrelated update", acpHistoryFields{SessionUpdate: "agent_message_chunk", ToolCallID: "tc-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, acpHistoryIsMergeableToolCall(tc.fields))
		})
	}
}

func TestAppendTerminalHistoryTextGuards(t *testing.T) {
	var nilAgent *ACPTunnelAgent
	assert.NoError(t, nilAgent.appendTerminalHistoryText(nil, "msg", "data"))

	agent := &ACPTunnelAgent{agentID: "a"}
	assert.NoError(t, agent.appendTerminalHistoryText(nil, "msg", ""), "empty delta is a no-op")
}

func updateObject(t *testing.T, frame json.RawMessage) map[string]any {
	t.Helper()
	var decoded struct {
		Params struct {
			Update map[string]any `json:"update"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(frame, &decoded))
	require.NotNil(t, decoded.Params.Update)
	return decoded.Params.Update
}

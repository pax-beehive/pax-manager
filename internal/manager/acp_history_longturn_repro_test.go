package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type longTurnHarness struct {
	t     *testing.T
	ctx   context.Context
	store *storage.MemoryStore
	agent *ACPTunnelAgent
	seq   int64
}

func newLongTurnHarness(t *testing.T) *longTurnHarness {
	t.Helper()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	})
	return &longTurnHarness{
		t:     t,
		ctx:   context.Background(),
		store: store,
		agent: &ACPTunnelAgent{
			agentID: "agent_lt", ownerUserID: "user_lt", nodeID: "node_lt",
			sessionID: "sess_lt", store: store,
		},
	}
}

func (h *longTurnHarness) project(raw string) {
	h.t.Helper()
	h.seq++
	r := json.RawMessage(raw)
	require.NoError(h.t, projectACPTransportMessage(h.ctx, h.store, h.agent.agentID,
		h.agent.ownerUserID, h.agent.nodeID, domain.TransportStreamPaxdToManager, h.seq,
		h.agent.historyGroupID(h.seq, r), r))
	h.agent.observeHistoryBoundary(r)
}

func (h *longTurnHarness) message(update, text string) {
	h.project(
		fmt.Sprintf(
			`{"method":"session/update","params":{"sessionId":"sess_lt","update":{"sessionUpdate":%q,"content":{"type":"text","text":%q}}},"jsonrpc":"2.0"}`,
			update,
			text,
		),
	)
}

func (h *longTurnHarness) toolCall(id, title string) {
	h.project(
		fmt.Sprintf(
			`{"method":"session/update","params":{"sessionId":"sess_lt","update":{"sessionUpdate":"tool_call","toolCallId":%q,"title":%q,"kind":"execute"}},"jsonrpc":"2.0"}`,
			id,
			title,
		),
	)
}

func (h *longTurnHarness) terminal(termID, data string) {
	h.project(string(terminalDeltaFrame("sess_lt", termID, data)))
}

func (h *longTurnHarness) rows() []domain.Message {
	h.t.Helper()
	all, err := h.store.ListMessages(h.ctx, h.agent.agentID, h.agent.sessionID, 1000)
	require.NoError(h.t, err)
	return all
}

func (h *longTurnHarness) text(m domain.Message) string {
	parts, err := h.store.ListMessageParts(h.ctx, m.MessageID)
	require.NoError(h.t, err)
	if len(parts) == 0 {
		return ""
	}
	return parts[0].Text
}

// TestLongTurnAnswerAfterTerminalOutputStaysDistinctAndLast is the regression
// for: a long turn returns a run of tool calls but the answer goes missing /
// out of order. Terminal output between message chunks must break the message
// grouping so the post-tool answer is its own row ordered after the tool
// output, not folded back into a pre-tool message frozen at an early id.
func TestLongTurnAnswerAfterTerminalOutputStaysDistinctAndLast(t *testing.T) {
	h := newLongTurnHarness(t)

	// A tool is already running; the agent comments, the tool streams terminal
	// output, then the agent gives the final answer. Without the group break,
	// "Here is the final answer." folds back into the "Looking at the build. "
	// row (frozen at an early id) because terminal deltas are text-projected.
	h.toolCall("tc-build", "run build")
	h.message("agent_message_chunk", "Looking at the build. ")
	for i := 0; i < 8; i++ {
		h.terminal("tc-build", fmt.Sprintf("build line %d\n", i))
	}
	h.message("agent_message_chunk", "Here is the final answer.")

	rows := h.rows()

	var opening, answer, terminal domain.Message
	for _, m := range rows {
		switch {
		case h.text(m) == "Looking at the build. ":
			opening = m
		case h.text(m) == "Here is the final answer.":
			answer = m
		case m.MessageType == "tool_call_update":
			terminal = m
		}
	}

	require.NotEmpty(t, answer.MessageID, "final answer must exist as its own row")
	assert.NotEqual(t, opening.MessageID, answer.MessageID,
		"answer must not fold back into the pre-tool message row")
	assert.Greater(t, answer.ID, terminal.ID,
		"answer must be ordered after the terminal output, not frozen before it")

	// The answer is the last substantive row, so it lands on the most-recent page.
	assert.Equal(t, answer.ID, rows[len(rows)-1].ID)
}

// TestLongTurnMergedToolUpdatesDoNotBuryAnswer covers the other half: a long
// tail of tool_call_update frames must collapse per toolCallId so they cannot
// push the answer off the first history page.
func TestLongTurnMergedToolUpdatesDoNotBuryAnswer(t *testing.T) {
	h := newLongTurnHarness(t)

	h.message("agent_message_chunk", "Working. ")
	for i := 0; i < 6; i++ {
		h.toolCall(fmt.Sprintf("tc-%d", i), "step")
	}
	h.message("agent_message_chunk", "Done, here is the answer.")
	// Heavy trailing status churn on the already-running tools.
	for round := 0; round < 40; round++ {
		for i := 0; i < 6; i++ {
			h.project(
				fmt.Sprintf(
					`{"method":"session/update","params":{"sessionId":"sess_lt","update":{"sessionUpdate":"tool_call_update","toolCallId":%q,"status":"completed"}},"jsonrpc":"2.0"}`,
					fmt.Sprintf("tc-%d", i),
				),
			)
		}
	}

	rows := h.rows()
	// 1 opening + 6 tools + 1 answer = 8 rows; 240 trailing updates add none.
	assert.Len(t, rows, 8, "trailing updates must not create new rows")

	page, err := h.store.ListMessageHistoryPage(h.ctx, h.agent.agentID, h.agent.sessionID, 0, 5)
	require.NoError(t, err)
	found := false
	for _, m := range page.Messages {
		if h.text(m) == "Done, here is the answer." {
			found = true
		}
	}
	assert.True(t, found, "answer must be on the first (most recent) page")
}

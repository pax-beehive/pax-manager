package manager

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestTaggedTurnPreservesTextToolTextSegments(t *testing.T) {
	for _, batched := range []bool{false, true} {
		t.Run(fmt.Sprintf("batched=%t", batched), func(t *testing.T) {
			h := newLongTurnHarness(t)
			var sink acpHistoryTextSink = immediateACPHistoryTextSink{store: h.store}
			if batched {
				sink = acpAgentHistoryTextSink{agent: h.agent}
			}
			project := func(turn, raw string) {
				t.Helper()
				h.seq++
				payload := json.RawMessage(raw)
				require.NoError(t, projectACPTransportMessageWithTextSinkForTurn(
					h.ctx,
					h.store,
					sink,
					h.agent.agentID,
					h.agent.ownerUserID,
					h.agent.nodeID,
					domain.TransportStreamPaxdToManager,
					h.seq,
					h.agent.historyGroupIDForSession(
						h.seq,
						"sess_lt",
						payload,
					),
					"sess_lt",
					turn,
					payload,
				))
			}
			text := func(turn, body string) {
				project(
					turn,
					fmt.Sprintf(
						`{"method":"session/update","params":{"sessionId":"sess_lt","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`,
						body,
					),
				)
			}
			text("turn_one", "A1")
			text("turn_one", "A2")
			project(
				"turn_one",
				`{"method":"session/update","params":{"sessionId":"sess_lt","update":{"sessionUpdate":"tool_call","toolCallId":"tool","title":"Run"}}}`,
			)
			project("turn_one", string(terminalDeltaFrame("sess_lt", "tool", "output")))
			text("turn_one", "B1")
			text("turn_one", "B2")
			// A different turn must not reuse the last text group's message row.
			text("turn_two", "C")
			require.NoError(t, sink.Flush(h.ctx))
			var messages []domain.Message
			for _, row := range h.rows() {
				if row.MessageType == "agent_message_chunk" {
					messages = append(messages, row)
				}
			}
			require.Len(t, messages, 3)
			require.Equal(t, "A1A2", h.text(messages[0]))
			require.Equal(t, "B1B2", h.text(messages[1]))
			require.Equal(t, "C", h.text(messages[2]))
			require.Equal(t, "turn_one", messages[1].TurnID)
			require.Equal(t, "turn_two", messages[2].TurnID)
			require.Greater(t, messages[1].SessionSeq, messages[0].SessionSeq+1)
			for _, row := range messages {
				require.JSONEq(t, `{"text_layout":"segment"}`, string(row.RawJSON))
			}
		})
	}
}

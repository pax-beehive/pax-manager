package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryCompletionGivenStreamingInputThenRequiresACompletedMessageOrMatchingTerminal(
	t *testing.T,
) {
	source := domain.Message{AgentID: "agent", SessionID: "session", TurnID: "turn", SessionSeq: 2}
	assert.False(t, secretarySourceComplete(source, domain.Message{}))
	terminal := domain.Message{
		AgentID:     "agent",
		SessionID:   "session",
		TurnID:      "turn",
		SessionSeq:  3,
		MessageType: "turn_done",
		Status:      "complete",
	}
	assert.True(t, secretarySourceComplete(source, terminal))
	terminal.TurnID = "other"
	assert.False(t, secretarySourceComplete(source, terminal))
	terminal.TurnID, terminal.MessageType, terminal.Status = "turn", "end_turn", ""
	terminal.RawJSON = json.RawMessage(`{"result":{"stopReason":"end_turn"}}`)
	assert.True(t, secretarySourceComplete(source, terminal))
	terminal.RawJSON = json.RawMessage(`{"result":{"stopReason":"max_tokens"}}`)
	assert.False(t, secretarySourceComplete(source, terminal))
	terminal.RawJSON = json.RawMessage(`{"note":"end_turn"}`)
	assert.False(t, secretarySourceComplete(source, terminal))
	terminal.RawJSON = json.RawMessage(`{`)
	assert.False(t, secretarySourceComplete(source, terminal))
	source.Status = "complete"
	assert.True(t, secretarySourceComplete(source, domain.Message{}))
}

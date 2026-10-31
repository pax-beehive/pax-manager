package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalSearchMessageGivenMultipartWhenRepartitionedThenIdentityAndHashStayStable(
	t *testing.T,
) {
	message := MessageWithParts{
		Message: Message{
			MessageID:   "message",
			Role:        "assistant",
			MessageType: "agent_message_chunk",
		},
		Parts: []MessagePart{{PartIndex: 1, PartType: MessagePartText, Text: "\u5e93"},
			{PartIndex: 0, PartType: MessagePartText, Text: "\u6570\u636e"},
			{PartIndex: 2, PartType: "artifact", Text: "private tool result"}},
	}
	first, err := CanonicalSearchMessage(message)
	require.NoError(t, err)
	assert.Equal(t, "\u6570\u636e\u5e93", first.Body)
	assert.Equal(t, "message:message", first.DocumentID())
	assert.Equal(t, 1, message.Parts[0].PartIndex, "normalization must not mutate stored parts")
	message.Parts = []MessagePart{{PartType: MessagePartText, Text: first.Body}}
	second, err := CanonicalSearchMessage(message)
	require.NoError(t, err)
	assert.Equal(t, first.ContentHash, second.ContentHash)
	assert.Equal(t, SearchCanonicalizationVersion, first.CanonicalizationVersion)
}

func TestCanonicalSearchMessageGivenInvisibleOrMalformedSourceThenRejectsIt(t *testing.T) {
	base := MessageWithParts{Message: Message{MessageID: "message", Role: "assistant"},
		Parts: []MessagePart{{PartType: MessagePartText, Text: "visible"}}}
	for _, test := range []struct {
		name   string
		change func(*MessageWithParts)
	}{
		{"missing identity", func(m *MessageWithParts) { m.MessageID = "" }},
		{"tool role", func(m *MessageWithParts) { m.Role = "tool" }},
		{"thought type", func(m *MessageWithParts) { m.MessageType = "agent_thought_chunk" }},
		{"tool type", func(m *MessageWithParts) { m.MessageType = "tool_call_update" }},
		{"thought envelope", func(m *MessageWithParts) {
			m.RawJSON = json.RawMessage(`{"params":{"update":{"sessionUpdate":"agent_thought_chunk"}}}`)
		}},
		{"analysis envelope", func(m *MessageWithParts) { m.RawJSON = json.RawMessage(`{"channel":"analysis"}`) }},
		{"invalid envelope", func(m *MessageWithParts) { m.RawJSON = json.RawMessage(`{`) }},
		{"empty text", func(m *MessageWithParts) { m.Parts = nil }},
		{"negative index", func(m *MessageWithParts) { m.Parts[0].PartIndex = -1 }},
		{"duplicate index", func(m *MessageWithParts) { m.Parts = append(m.Parts, m.Parts[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := base
			message.Parts = append([]MessagePart(nil), base.Parts...)
			test.change(&message)
			_, err := CanonicalSearchMessage(message)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	}
	base.RawJSON = json.RawMessage(`{"params":{"update":{"sessionUpdate":"agent_message_chunk"}}}`)
	_, err := CanonicalSearchMessage(base)
	assert.NoError(t, err)
}

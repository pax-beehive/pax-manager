package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchMessageNodeIDPreservesOriginAndRejectsConflicts(t *testing.T) {
	for _, tc := range []struct{ message, session, want string }{
		{"node-a", "node-a", "node-a"}, {"", "node-a", "node-a"},
		{"node-a", "", "node-a"}, {"", "", ""}, {"node-a", "node-b", ""},
	} {
		assert.Equal(t, tc.want, SearchMessageNodeID(tc.message, tc.session))
	}
	doc, err := CanonicalSearchMessage(MessageWithParts{
		Message: Message{MessageID: "message", NodeID: "node-a", Role: "user"},
		Parts:   []MessagePart{{PartType: MessagePartText, Text: "visible"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "node-a", doc.NodeID)
}

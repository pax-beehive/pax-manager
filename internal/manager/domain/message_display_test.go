package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalTranscriptMessagesGivenPaxInvocationThenReplacesParent(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"replaces_message_ids": []string{"msg_real"},
	})
	require.NoError(t, err)
	messages := []MessageWithParts{
		{
			Message: Message{
				MessageID:   "msg_real",
				MessageType: MessageTypePaxUser,
			},
			Parts: []MessagePart{{Text: "real prompt"}},
		},
		{
			Message: Message{
				MessageID:       "msg_display",
				MessageType:     MessageTypePaxInvocation,
				ParentMessageID: "msg_real",
				RawJSON:         raw,
			},
			Parts: []MessagePart{{Text: "display prompt"}},
		},
		{
			Message: Message{
				MessageID:   "msg_reply",
				MessageType: "message:delta",
			},
			Parts: []MessagePart{{Text: "reply"}},
		},
	}

	normal := NormalTranscriptMessages(messages)

	require.Len(t, normal, 2)
	assert.Equal(t, "msg_display", normal[0].MessageID)
	assert.Equal(t, "display prompt", normal[0].Parts[0].Text)
	assert.Equal(t, "msg_reply", normal[1].MessageID)
}

func TestNormalTranscriptMessagesGivenEmptyReplacementThenHidesParent(t *testing.T) {
	messages := []MessageWithParts{
		{Message: Message{MessageID: "msg_real", MessageType: MessageTypePaxUser}},
		{
			Message: Message{
				MessageID:       "msg_display",
				MessageType:     MessageTypePaxInvocation,
				ParentMessageID: "msg_real",
				RawJSON:         json.RawMessage(`{}`),
			},
		},
	}

	normal := NormalTranscriptMessages(messages)

	require.Len(t, normal, 1)
	assert.Equal(t, "msg_display", normal[0].MessageID)
}

func TestNormalTranscriptMessagesGivenPendingInvocationThenShowsPendingAndHidesReplacedRows(
	t *testing.T,
) {
	messages := []MessageWithParts{
		{Message: Message{MessageID: "msg_tool", MessageType: "tool_call"}},
		{Message: Message{MessageID: "msg_prompt", MessageType: MessageTypePaxUser}},
		{
			Message: Message{
				MessageID:       "msg_pending",
				MessageType:     MessageTypePaxInvocationPending,
				ParentMessageID: "msg_tool",
				RawJSON: json.RawMessage(
					`{"replaces_message_ids":["msg_tool","msg_prompt"]}`,
				),
			},
			Parts: []MessagePart{{Text: "Asked target for input."}},
		},
		{Message: Message{MessageID: "msg_visible", MessageType: "message:delta"}},
	}

	normal := NormalTranscriptMessages(messages)

	require.Len(t, normal, 2)
	assert.Equal(t, "msg_pending", normal[0].MessageID)
	assert.Equal(t, "Asked target for input.", normal[0].Parts[0].Text)
	assert.Equal(t, "msg_visible", normal[1].MessageID)
}

func TestNormalTranscriptGivenArtifactSourceOutsidePageThenPlacesCardWithinItsTurn(t *testing.T) {
	messages := []MessageWithParts{
		{Message: Message{MessageID: "prompt", SessionSeq: 1}},
		{Message: Message{MessageID: "answer", SessionSeq: 5}},
		{Message: Message{MessageID: "done", SessionSeq: 6}},
		{Message: Message{MessageID: "next_prompt", SessionSeq: 7}},
		{Message: Message{
			MessageID: "artifact", MessageType: MessageTypePaxArtifact, SessionSeq: 8,
			ParentMessageID: "source", RawJSON: json.RawMessage(`{"source_session_seq":3}`),
		}},
	}
	normal := NormalTranscriptMessages(messages)
	var ids []string
	for _, message := range normal {
		ids = append(ids, message.MessageID)
	}
	assert.Equal(t, []string{"prompt", "artifact", "answer", "done", "next_prompt"}, ids)
	assert.Equal(t, int64(8), normal[1].SessionSeq)
}

func TestNormalTranscriptGivenArtifactAndInvocationThenPreservesBothSourcePositions(t *testing.T) {
	messages := []MessageWithParts{
		{Message: Message{MessageID: "invocation_source", SessionSeq: 1}},
		{Message: Message{MessageID: "artifact_source", SessionSeq: 2}},
		{Message: Message{MessageID: "done", SessionSeq: 3}},
		{Message: Message{
			MessageID: "invocation", MessageType: MessageTypePaxInvocation, SessionSeq: 4,
			ParentMessageID: "invocation_source",
		}},
		{Message: Message{
			MessageID: "artifact", MessageType: MessageTypePaxArtifact, SessionSeq: 5,
			ParentMessageID: "artifact_source", RawJSON: json.RawMessage(`{"source_session_seq":2}`),
		}},
	}
	var ids []string
	for _, message := range NormalTranscriptMessages(messages) {
		ids = append(ids, message.MessageID)
	}
	assert.Equal(t, []string{"invocation", "artifact", "done"}, ids)
}

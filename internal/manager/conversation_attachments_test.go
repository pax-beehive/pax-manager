package manager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConversationAttachmentGivenAgentNodeChangesThenSameUUIDBuildsLocalResourceLink(
	t *testing.T,
) {
	srv, _ := testServer(t, "conversation-attachment@example.com")
	principal := UserPrincipal{User: User{UserID: "user_conversation_attachment"}}
	attachment, err := srv.store.CreateUserAttachment(
		context.Background(),
		principal,
		CreateUserAttachmentRequest{
			Filename:    "notes.txt",
			ContentType: "text/plain",
			SizeBytes:   5,
		},
		"attachments",
		"user-attachments/user_conversation_attachment/notes.txt",
		time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	attachment, err = srv.store.CompleteUserAttachment(
		context.Background(),
		principal,
		attachment.AttachmentID,
		ArtifactContent{ContentType: "text/plain", SizeBytes: 5, Generation: 7},
	)
	require.NoError(t, err)

	for _, nodeID := range []string{"node_before_switch", "node_after_switch"} {
		localURI := "file:///tmp/" + nodeID + "/" + attachment.AttachmentID + "/notes.txt"
		srv.nodeControls.ObserveAttachmentState(nodeID, nodeControlAttachmentLocalState{
			AttachmentID: attachment.AttachmentID,
			State:        "ready",
			LocalURI:     localURI,
		})
		prompt, err := srv.resolveConversationPrompt(
			context.Background(),
			principal,
			nodeID,
			conversationRequest{Content: []conversationContentBlock{
				{Type: "text", Text: "read this"},
				{Type: "attachment", AttachmentID: attachment.AttachmentID},
			}},
		)
		require.NoError(t, err)
		require.Equal(t, []map[string]any{
			{"type": "text", "text": "read this"},
			{
				"type":     "resource_link",
				"uri":      localURI,
				"name":     "notes.txt",
				"mimeType": "text/plain",
			},
		}, prompt)
	}
}

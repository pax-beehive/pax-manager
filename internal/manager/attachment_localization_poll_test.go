package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestAttachmentLocalizationGivenReadyReportIsLostWhenPollRunsThenItReturnsLocalURI(
	t *testing.T,
) {
	srv, _ := testServer(t, "attachment-local@example.com")
	srv.paxdArtifacts = &fakePaxdArtifactBackend{}
	principal := UserPrincipal{User: User{UserID: "user_attachment_local"}}
	attachment, err := srv.store.CreateUserAttachment(
		context.Background(),
		principal,
		CreateUserAttachmentRequest{
			Filename:    "notes.txt",
			ContentType: "text/plain",
			SizeBytes:   5,
		},
		"attachments",
		"user-attachments/user_attachment_local/notes.txt",
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

	ws := newFakeNodeControlWebSocket()
	conn := newNodeControlConnection("node_1", ws)
	srv.nodeControls.Add("node_1", conn)
	t.Cleanup(func() { srv.nodeControls.Remove("node_1", conn) })

	originalPoll := attachmentStatusPollInterval
	originalPush := attachmentPushCheckInterval
	attachmentStatusPollInterval = 20 * time.Millisecond
	attachmentPushCheckInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		attachmentStatusPollInterval = originalPoll
		attachmentPushCheckInterval = originalPush
	})

	resultCh := make(chan map[string]attachmentLocalizationResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, ensureErr := srv.ensureAttachmentsLocal(
			context.Background(),
			principal,
			"node_1",
			[]string{attachment.AttachmentID},
		)
		resultCh <- result
		errCh <- ensureErr
	}()

	initialQuery := decodeNodeControlOutgoing(t, ws.nextWrite(t))
	require.Equal(t, "query", initialQuery.Kind)
	require.NoError(t, deliverNodeControlQueryResult(
		conn,
		initialQuery.RequestID,
		attachment.AttachmentID,
		"unknown",
		"",
	))

	ensureCommand := decodeNodeControlOutgoing(t, ws.nextWrite(t))
	require.Equal(t, "command", ensureCommand.Kind)
	require.NotContains(t, string(ensureCommand.Command), "agent_id")
	require.NoError(t, deliverNodeControlCommandAck(conn, ensureCommand.CommandID))

	statusPoll := decodeNodeControlOutgoing(t, ws.nextWrite(t))
	require.Equal(t, "query", statusPoll.Kind)
	localURI := "file:///tmp/attachments/" + attachment.AttachmentID + "/notes.txt"
	require.NoError(t, deliverNodeControlQueryResult(
		conn,
		statusPoll.RequestID,
		attachment.AttachmentID,
		"ready",
		localURI,
	))

	require.NoError(t, <-errCh)
	result := <-resultCh
	require.Equal(t, localURI, result[attachment.AttachmentID].State.LocalURI)
	require.Equal(
		t,
		int64(7),
		srv.paxdArtifacts.(*fakePaxdArtifactBackend).signedArtifact.Generation,
	)
	require.Equal(
		t,
		domain.UserAttachmentUploadCompleted,
		result[attachment.AttachmentID].Attachment.UploadStatus,
	)
}

type nodeControlOutgoingForAttachmentTest struct {
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	CommandID string          `json:"command_id"`
	Command   json.RawMessage `json:"command"`
}

func decodeNodeControlOutgoing(t *testing.T, payload []byte) nodeControlOutgoingForAttachmentTest {
	t.Helper()
	var frame nodeControlOutgoingForAttachmentTest
	require.NoError(t, json.Unmarshal(payload, &frame))
	return frame
}

func deliverNodeControlQueryResult(
	conn *nodeControlConnection,
	requestID string,
	attachmentID string,
	state string,
	localURI string,
) error {
	payload, err := json.Marshal(map[string]any{
		"kind":       "response",
		"request_id": requestID,
		"query_result": map[string]any{
			"type": "attachment.local_status",
			"attachment_local_status": map[string]any{
				"items": []map[string]any{{
					"attachment_id": attachmentID,
					"state":         state,
					"local_uri":     localURI,
				}},
			},
		},
	})
	if err != nil {
		return err
	}
	_, err = conn.HandleIncoming(payload)
	return err
}

func deliverNodeControlCommandAck(conn *nodeControlConnection, commandID string) error {
	payload, err := json.Marshal(map[string]any{
		"kind":       "ack",
		"command_id": commandID,
		"command_ack": map[string]any{
			"command_id": commandID,
			"ok":         true,
			"status":     "received",
		},
	})
	if err != nil {
		return err
	}
	_, err = conn.HandleIncoming(payload)
	return err
}

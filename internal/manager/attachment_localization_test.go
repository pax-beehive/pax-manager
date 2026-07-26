package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNodeControlAttachmentReadyReportUpdatesEphemeralLocalizationState(t *testing.T) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	err := srv.handleNodeControlTunnelFrame(context.Background(), Node{
		NodeID: registered.NodeID,
	}, []byte(`{
		"kind":"report",
		"version":1,
		"report":{
			"type":"attachment.local_state",
			"node_id":"`+registered.NodeID+`",
			"attachment_local_state":{
				"attachment_id":"att_1",
				"state":"ready",
				"local_uri":"file:///tmp/attachments/att_1/notes.txt",
				"size_bytes":5
			}
		}
	}`))
	require.NoError(t, err)

	state, ok := srv.nodeControls.AttachmentState(registered.NodeID, "att_1")
	require.True(t, ok)
	require.Equal(t, "ready", state.State)
	require.Equal(t, "file:///tmp/attachments/att_1/notes.txt", state.LocalURI)
}

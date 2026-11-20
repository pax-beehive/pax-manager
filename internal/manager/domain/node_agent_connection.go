package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	// NodeAgentConnectionStateDeleted is the desired_state paxd reports for a
	// connection that was removed on the node.
	NodeAgentConnectionStateDeleted = "deleted"
	// NodeDaemonCommandAgentConnectionDelete removes a paxd agent connection.
	NodeDaemonCommandAgentConnectionDelete = "agent_connection.delete"
)

// NodeAgentConnectionRef names a paxd agent connection and the cloud agent it
// is bound to.
type NodeAgentConnectionRef struct {
	AgentID      string
	ConnectionID string
}

// DeletedAgentConnectionCommandID is the idempotency key for removing the paxd
// connection of a deleted agent. It is stable per connection so repeated
// reconcile passes replay one paxd command instead of creating new ones.
func DeletedAgentConnectionCommandID(ref NodeAgentConnectionRef) string {
	sum := sha256.Sum256([]byte(ref.AgentID + "\n" + ref.ConnectionID))
	return "agentdel_" + hex.EncodeToString(sum[:16])
}

// DeleteNodeAgentConnectionCommand builds the paxd control command that
// removes one agent connection.
func DeleteNodeAgentConnectionCommand(commandID string, connectionID string) map[string]any {
	return map[string]any{
		"command_id": commandID,
		"type":       NodeDaemonCommandAgentConnectionDelete,
		"delete_agent_connection": map[string]any{
			"connection_id": connectionID,
		},
	}
}

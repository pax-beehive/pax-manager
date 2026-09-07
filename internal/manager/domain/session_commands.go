package domain

import (
	"context"
	"encoding/json"
	"time"
)

// SessionACPCommands preserves the agent's complete advertised command metadata.
type SessionACPCommands struct {
	AvailableCommands []json.RawMessage `json:"available_commands"`
	ObservedAt        time.Time         `json:"observed_at"`
}

type SessionACPCommandsStore interface {
	UpdateSessionACPCommands(
		ctx context.Context,
		agentID, sessionID string,
		commands SessionACPCommands,
	) error
}

func SessionACPCommandsFromMetadata(metadata json.RawMessage) *SessionACPCommands {
	var value struct {
		Commands *SessionACPCommands `json:"acp_commands"`
	}
	if json.Unmarshal(metadata, &value) != nil {
		return nil
	}
	return value.Commands
}

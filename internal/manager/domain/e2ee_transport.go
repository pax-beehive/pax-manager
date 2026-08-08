package domain

import (
	"context"
	"time"
)

type E2EERecord struct {
	RecordID        string    `json:"record_id"`
	OwnerUserID     string    `json:"-"`
	NodeID          string    `json:"node_id,omitempty"`
	AgentID         string    `json:"agent_id"`
	SessionID       string    `json:"session_id"`
	Kind            string    `json:"kind"`
	ProtocolVersion int       `json:"protocol_version"`
	CipherVersion   int       `json:"cipher_version"`
	KeyEpoch        int64     `json:"key_epoch"`
	Nonce           []byte    `json:"-"`
	Ciphertext      []byte    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
}

type AgentCommand struct {
	ID int64
	E2EERecord
	DeliveredAt       *time.Time
	DeliveredEpoch    *int64
	AcknowledgedAt    *time.Time
	AcknowledgedEpoch *int64
	ExpiresAt         *time.Time
}

type AgentEvent struct {
	Cursor int64 `json:"cursor"`
	E2EERecord
}

type E2EETransportStore interface {
	CreateAgentCommand(ctx context.Context, command AgentCommand) (AgentCommand, bool, error)
	ListPendingAgentCommands(
		ctx context.Context,
		agentID string,
		connectionEpoch int64,
		limit int,
	) ([]AgentCommand, error)
	MarkAgentCommandDelivered(ctx context.Context, commandID string, connectionEpoch int64) error
	AcknowledgeAgentCommand(ctx context.Context, commandID string, connectionEpoch int64) error
	InsertAgentEvent(ctx context.Context, event AgentEvent) (AgentEvent, bool, error)
	ListAgentEvents(
		ctx context.Context,
		ownerUserID string,
		sessionID string,
		afterCursor int64,
		limit int,
	) ([]AgentEvent, error)
	RegisterAgentConnection(ctx context.Context, agentID string) (int64, error)
	CurrentAgentConnectionEpoch(ctx context.Context, agentID string) (int64, error)
}

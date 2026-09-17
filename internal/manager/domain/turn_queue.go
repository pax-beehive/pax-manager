package domain

import (
	"context"
	"time"
)

const MaxQueuedTurnBytes = 64 * 1024

// QueuedTurn is a single pending slot, not execution history.
type QueuedTurn struct {
	TurnID    string    `json:"queued_turn_id"`
	CommandID string    `json:"command_id"`
	OwnerID   string    `json:"-"`
	NodeID    string    `json:"node_id,omitempty"`
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	NativeID  string    `json:"-"`
	Input     string    `json:"input"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TurnQueueStore interface {
	PutQueuedTurn(context.Context, QueuedTurn, bool) (QueuedTurn, bool, error)
	GetQueuedTurn(context.Context, string, string) (QueuedTurn, error)
	DeleteQueuedTurn(context.Context, string, string) (QueuedTurn, error)
	ListQueuedTurns(context.Context, string, string) ([]QueuedTurn, error)
	ClaimQueuedTurn(context.Context, QueuedTurn, AgentRuntimeSnapshot) (bool, error)
	FinishQueuedTurn(context.Context, QueuedTurn, bool) error
}

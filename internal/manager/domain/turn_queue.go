package domain

import (
	"context"
	"time"
)

const (
	MaxQueuedTurnBytes       = 64 * 1024
	MaxQueuedTurnAttachments = 20
)

// QueuedTurn is a single pending slot, not execution history.
type QueuedTurn struct {
	TurnID       string                 `json:"queued_turn_id"`
	CommandID    string                 `json:"command_id"`
	OwnerID      string                 `json:"-"`
	NodeID       string                 `json:"node_id,omitempty"`
	AgentID      string                 `json:"agent_id"`
	SessionID    string                 `json:"session_id"`
	NativeID     string                 `json:"-"`
	Input        string                 `json:"input"`
	Attachments  []QueuedTurnAttachment `json:"attachments,omitempty"`
	SearchAccess *WorkspaceSearchAccess `json:"search_access,omitempty"`
	State        string                 `json:"state"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// QueuedTurnAttachment snapshots what the composer shows for a queued file.
// The attachment is authorized again and localized on the node when the
// turn runs.
type QueuedTurnAttachment struct {
	AttachmentID string `json:"attachment_id"`
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
}

// ValidQueuedTurnPrompt reports whether a queued prompt has text or files and
// stays within the slot bounds.
func ValidQueuedTurnPrompt(q QueuedTurn) bool {
	return (q.Input != "" || len(q.Attachments) > 0) &&
		len(q.Input) <= MaxQueuedTurnBytes &&
		len(q.Attachments) <= MaxQueuedTurnAttachments
}

type TurnQueueStore interface {
	PutQueuedTurn(context.Context, QueuedTurn, bool) (QueuedTurn, bool, error)
	GetQueuedTurn(context.Context, string, string) (QueuedTurn, error)
	DeleteQueuedTurn(context.Context, string, string) (QueuedTurn, error)
	ListQueuedTurns(context.Context, string, string) ([]QueuedTurn, error)
	ClaimQueuedTurn(context.Context, QueuedTurn, AgentRuntimeSnapshot) (bool, error)
	FinishQueuedTurn(context.Context, QueuedTurn, bool) error
}

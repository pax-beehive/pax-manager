package domain

import (
	"context"
	"encoding/json"
	"time"
)

// Node daemon command types that pax-manager records durably.
const (
	NodeDaemonCommandPaxdRestart       = "paxd.restart"
	NodeDaemonCommandPaxdUpgrade       = "paxd.upgrade"
	NodeDaemonCommandPaxdCancel        = "paxd.maintenance.cancel"
	NodeDaemonCommandPaxlUpgrade       = "paxl.upgrade"
	NodeDaemonCommandHarnessUpgrade    = "harness.upgrade"
	NodeDaemonCommandStateInProgress   = "in_progress"
	NodeDaemonCommandStateSucceeded    = "succeeded"
	NodeDaemonCommandStateFailed       = "failed"
	NodeDaemonCommandStatusDispatching = "dispatching"
	// NodeDaemonCommandStatusDispatchFailed means paxd never acknowledged the
	// command, so it never ran.
	NodeDaemonCommandStatusDispatchFailed = "dispatch_failed"
	// NodeDaemonCommandStatusLost means paxd acknowledged the command earlier
	// but no longer knows it, for example after its local state was reset.
	NodeDaemonCommandStatusLost = "lost"
)

// NodeDaemonCommand is the manager-side record of a maintenance command sent
// to paxd. The manager owns the intent fields; paxd owns execution, mirrored
// in Status, Phase, Error*, Result and MaintenanceConfirmation.
type NodeDaemonCommand struct {
	CommandID               string                             `json:"command_id"`
	NodeID                  string                             `json:"node_id"`
	OwnerUserID             string                             `json:"-"`
	RequestedByUserID       string                             `json:"requested_by_user_id,omitempty"`
	Type                    string                             `json:"type"`
	Target                  json.RawMessage                    `json:"target,omitempty"`
	TargetVersion           string                             `json:"target_version,omitempty"`
	State                   string                             `json:"state"`
	Status                  string                             `json:"status"`
	Phase                   string                             `json:"phase,omitempty"`
	ErrorCode               string                             `json:"error_code,omitempty"`
	ErrorMessage            string                             `json:"error_message,omitempty"`
	Result                  json.RawMessage                    `json:"result,omitempty"`
	MaintenanceConfirmation *NodeDaemonMaintenanceConfirmation `json:"maintenance_confirmation,omitempty"`
	CreatedAt               time.Time                          `json:"created_at"`
	UpdatedAt               time.Time                          `json:"updated_at"`
	ObservedAt              *time.Time                         `json:"observed_at,omitempty"`
	CompletedAt             *time.Time                         `json:"completed_at,omitempty"`
}

type NodeDaemonCommandFilter struct {
	Type  string
	State string
	Limit int
}

type NodeDaemonCommandStore interface {
	// CreateNodeDaemonCommand inserts the record unless (node_id, command_id)
	// already exists, and returns the stored record either way.
	CreateNodeDaemonCommand(
		ctx context.Context,
		command NodeDaemonCommand,
	) (NodeDaemonCommand, bool, error)
	// SaveNodeDaemonCommand replaces the execution fields of an existing record.
	SaveNodeDaemonCommand(ctx context.Context, command NodeDaemonCommand) error
	GetNodeDaemonCommand(
		ctx context.Context,
		nodeID string,
		commandID string,
	) (NodeDaemonCommand, error)
	ListNodeDaemonCommands(
		ctx context.Context,
		nodeID string,
		filter NodeDaemonCommandFilter,
	) ([]NodeDaemonCommand, error)
	// LatestNodeDaemonCommands returns the newest record per command type for
	// each requested node.
	LatestNodeDaemonCommands(
		ctx context.Context,
		nodeIDs []string,
	) (map[string][]NodeDaemonCommand, error)
}

// IsRecordedNodeDaemonCommand reports whether commands of this type are
// persisted as NodeDaemonCommand records.
func IsRecordedNodeDaemonCommand(commandType string) bool {
	switch commandType {
	case NodeDaemonCommandPaxdRestart,
		NodeDaemonCommandPaxdUpgrade,
		NodeDaemonCommandPaxdCancel,
		NodeDaemonCommandPaxlUpgrade,
		NodeDaemonCommandHarnessUpgrade:
		return true
	default:
		return false
	}
}

// NodeDaemonCommandState folds paxd status and, for restart and upgrade, the
// heartbeat-based maintenance confirmation into one coarse state. paxd keeps
// restart and upgrade records at "received" after the new process starts, so
// for those the confirmation decides completion.
func NodeDaemonCommandState(command NodeDaemonCommand) string {
	switch command.Status {
	case NodeDaemonCommandStatusDispatchFailed, NodeDaemonCommandStatusLost, "failed", "rejected":
		return NodeDaemonCommandStateFailed
	}
	if command.Type == NodeDaemonCommandPaxdRestart ||
		command.Type == NodeDaemonCommandPaxdUpgrade {
		if command.MaintenanceConfirmation != nil {
			switch command.MaintenanceConfirmation.Status {
			case "confirmed":
				return NodeDaemonCommandStateSucceeded
			case "version_mismatch":
				return NodeDaemonCommandStateFailed
			}
			return NodeDaemonCommandStateInProgress
		}
	}
	if command.Status == "applied" {
		return NodeDaemonCommandStateSucceeded
	}
	return NodeDaemonCommandStateInProgress
}

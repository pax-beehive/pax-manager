package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	RuntimeLifecycleIdle            = "idle"
	RuntimeLifecycleRunning         = "running"
	RuntimeLifecycleWaitingApproval = "waiting_approval"
	RuntimeLifecycleBlocked         = "blocked"
	RuntimeLifecycleCancelling      = "cancelling"
	RuntimeLifecycleErrored         = "errored"

	RuntimeBlockedReasonToolApproval = "tool_approval"
	RuntimeBlockedReasonUnknown      = "unknown"

	RuntimeStatusIdle            = "idle"
	RuntimeStatusRunning         = "running"
	RuntimeStatusWaitingApproval = "waiting_approval"

	RuntimeAuthorityFrames   = "frames"
	RuntimeAuthoritySnapshot = "snapshot"

	RuntimeSnapshotApplied   = "applied"
	RuntimeSnapshotDuplicate = "duplicate"
	RuntimeSnapshotStale     = "stale"
	RuntimeSnapshotFenced    = "fenced"
)

var ErrInvalidRuntimeSnapshot = errors.New("invalid runtime snapshot")

type RuntimeToolCall struct {
	ToolCallID string          `json:"tool_call_id"`
	Kind       string          `json:"kind,omitempty"`
	Title      string          `json:"title,omitempty"`
	Status     string          `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"raw_input,omitempty"`
}

type SessionRuntimeState struct {
	OwnerUserID           string            `json:"owner_user_id,omitempty"`
	NodeID                string            `json:"node_id,omitempty"`
	AgentID               string            `json:"agent_id"`
	SessionID             string            `json:"session_id"`
	Lifecycle             string            `json:"lifecycle"`
	ActiveTurnID          string            `json:"active_turn_id,omitempty"`
	ActivePromptRequestID string            `json:"active_prompt_request_id,omitempty"`
	TurnInstanceID        string            `json:"turn_instance_id,omitempty"`
	BlockedReason         string            `json:"blocked_reason,omitempty"`
	BlockedRef            string            `json:"blocked_ref,omitempty"`
	ActiveToolCalls       []RuntimeToolCall `json:"active_tool_calls,omitempty"`
	PendingApprovalID     string            `json:"pending_approval_id,omitempty"`
	LastStopReason        string            `json:"last_stop_reason,omitempty"`
	LastError             string            `json:"last_error,omitempty"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

type ActiveTurnSnapshot struct {
	NativeSessionID   string          `json:"native_session_id"`
	TurnInstanceID    string          `json:"turn_instance_id"`
	PromptRequestID   json.RawMessage `json:"prompt_request_id"`
	RuntimeStatus     string          `json:"runtime_status"`
	PendingApprovalID string          `json:"pending_approval_id,omitempty"`
}

type AgentRuntimeSnapshot struct {
	AgentID         string               `json:"agent_id"`
	ConnectionFence string               `json:"-"`
	Sequence        int64                `json:"sequence"`
	GeneratedAt     time.Time            `json:"generated_at"`
	ActiveTurns     []ActiveTurnSnapshot `json:"active_turns"`
}

type RuntimeStatusChange struct {
	SessionID       string `json:"session_id"`
	NativeSessionID string `json:"native_session_id"`
	RuntimeStatus   string `json:"runtime_status"`
	TurnInstanceID  string `json:"turn_instance_id,omitempty"`
}

type ReplaceAgentActiveTurnsResult struct {
	Status           string                `json:"status"`
	AuthorityChanged bool                  `json:"authority_changed"`
	Changes          []RuntimeStatusChange `json:"changes,omitempty"`
}

type SessionRuntimeSnapshotStore interface {
	ActivateNodeRuntimeFence(ctx context.Context, node Node, fence string) error
	ReplaceAgentActiveTurns(
		ctx context.Context,
		node Node,
		snapshot AgentRuntimeSnapshot,
	) (ReplaceAgentActiveTurnsResult, error)
}

func (s AgentRuntimeSnapshot) Validate() error {
	if strings.TrimSpace(s.AgentID) == "" {
		return fmt.Errorf("%w: agent_id is required", ErrInvalidRuntimeSnapshot)
	}
	if strings.TrimSpace(s.ConnectionFence) == "" {
		return fmt.Errorf("%w: connection fence is required", ErrInvalidRuntimeSnapshot)
	}
	if s.Sequence <= 0 {
		return fmt.Errorf("%w: sequence must be positive", ErrInvalidRuntimeSnapshot)
	}
	nativeSessions := make(map[string]struct{}, len(s.ActiveTurns))
	turnInstances := make(map[string]struct{}, len(s.ActiveTurns))
	for index, turn := range s.ActiveTurns {
		if err := turn.validate(); err != nil {
			return fmt.Errorf("%w: active_turns[%d]: %v", ErrInvalidRuntimeSnapshot, index, err)
		}
		if _, exists := nativeSessions[turn.NativeSessionID]; exists {
			return fmt.Errorf("%w: duplicate native_session_id", ErrInvalidRuntimeSnapshot)
		}
		if _, exists := turnInstances[turn.TurnInstanceID]; exists {
			return fmt.Errorf("%w: duplicate turn_instance_id", ErrInvalidRuntimeSnapshot)
		}
		nativeSessions[turn.NativeSessionID] = struct{}{}
		turnInstances[turn.TurnInstanceID] = struct{}{}
	}
	return nil
}

func (t ActiveTurnSnapshot) validate() error {
	if strings.TrimSpace(t.NativeSessionID) == "" {
		return errors.New("native_session_id is required")
	}
	if strings.TrimSpace(t.TurnInstanceID) == "" {
		return errors.New("turn_instance_id is required")
	}
	if t.RuntimeStatus != RuntimeStatusRunning && t.RuntimeStatus != RuntimeStatusWaitingApproval {
		return errors.New("runtime_status must be running or waiting_approval")
	}
	decoder := json.NewDecoder(bytes.NewReader(t.PromptRequestID))
	decoder.UseNumber()
	var requestID any
	if err := decoder.Decode(&requestID); err != nil {
		return errors.New("prompt_request_id must be valid JSON")
	}
	switch value := requestID.(type) {
	case string:
		if value == "" {
			return errors.New("prompt_request_id must not be empty")
		}
	case json.Number:
		if value.String() == "" {
			return errors.New("prompt_request_id must not be empty")
		}
	default:
		return errors.New("prompt_request_id must be a string or number")
	}
	return nil
}

func NormalizeRuntimeStatus(lifecycle string) string {
	switch lifecycle {
	case RuntimeLifecycleWaitingApproval:
		return RuntimeStatusWaitingApproval
	case RuntimeLifecycleRunning, RuntimeLifecycleBlocked, RuntimeLifecycleCancelling:
		return RuntimeStatusRunning
	default:
		return RuntimeStatusIdle
	}
}

func RuntimePromptRequestID(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(bytes.TrimSpace(raw))
}

func (s SessionRuntimeState) StatusSummary() (status string, currentTask string, runID string, runStatus string) {
	status = s.Lifecycle
	if status == "" {
		status = RuntimeLifecycleIdle
	}
	if len(s.ActiveToolCalls) > 0 {
		currentTask = firstNonEmptyRuntime(
			s.ActiveToolCalls[len(s.ActiveToolCalls)-1].Title,
			s.ActiveToolCalls[len(s.ActiveToolCalls)-1].Kind,
		)
	}
	if currentTask == "" && s.BlockedReason != "" {
		currentTask = s.BlockedReason
	}
	runID = firstNonEmptyRuntime(s.ActiveTurnID, s.ActivePromptRequestID)
	runStatus = status
	return status, currentTask, runID, runStatus
}

func firstNonEmptyRuntime(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

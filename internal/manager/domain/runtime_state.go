package domain

import (
	"encoding/json"
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
)

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
	BlockedReason         string            `json:"blocked_reason,omitempty"`
	BlockedRef            string            `json:"blocked_ref,omitempty"`
	ActiveToolCalls       []RuntimeToolCall `json:"active_tool_calls,omitempty"`
	PendingApprovalID     string            `json:"pending_approval_id,omitempty"`
	LastStopReason        string            `json:"last_stop_reason,omitempty"`
	LastError             string            `json:"last_error,omitempty"`
	UpdatedAt             time.Time         `json:"updated_at"`
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

package domain

import (
	"context"
	"time"
)

type SecretaryTaskQuery struct {
	Limit  int
	Cursor string
}

type SecretaryTask struct {
	TaskID             string     `json:"task_id"`
	SessionID          string     `json:"session_id"`
	SourceAgentID      string     `json:"source_agent_id"`
	Title              string     `json:"title"`
	SecretaryAgentID   string     `json:"secretary_agent_id"`
	Revision           int64      `json:"revision"`
	SettingsRevision   int64      `json:"settings_revision"`
	RetryEpoch         int64      `json:"retry_epoch"`
	BatchID            string     `json:"batch_id"`
	AttemptID          string     `json:"attempt_id"`
	ExecutionState     string     `json:"execution_state"`
	Paused             bool       `json:"paused"`
	AttemptCount       int        `json:"attempt_count"`
	ConnectionFailures int        `json:"connection_failures"`
	NextRetryAt        *time.Time `json:"next_retry_at,omitempty"`
	CreatedAt          *time.Time `json:"created_at,omitempty"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	ExecutionSessionID string     `json:"execution_session_id,omitempty"`
	ExecutionAgentID   string     `json:"execution_agent_id,omitempty"`
	ExecutionStatus    string     `json:"execution_status,omitempty"`
	LeaseActive        bool       `json:"lease_active"`
}

type SecretaryTaskAttempt struct {
	AttemptID          string    `json:"attempt_id"`
	BatchID            string    `json:"batch_id"`
	State              string    `json:"state"`
	NotDispatched      bool      `json:"not_dispatched"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	HasOutput          bool      `json:"has_output"`
	ErrorCode          string    `json:"error_code,omitempty"`
	ErrorMessage       string    `json:"error_message,omitempty"`
	ExecutionSessionID string    `json:"execution_session_id,omitempty"`
	ExecutionAgentID   string    `json:"execution_agent_id,omitempty"`
	ExecutionStatus    string    `json:"execution_status,omitempty"`
}

type SecretaryTaskPage struct {
	Items      []SecretaryTask `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type SecretaryTaskDetail struct {
	Task       SecretaryTask          `json:"task"`
	Attempts   []SecretaryTaskAttempt `json:"attempts"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type SecretaryTaskAction struct {
	Action           string `json:"action"`
	ExpectedRevision int64  `json:"expected_revision"`
	BatchID          string `json:"batch_id"`
	AttemptID        string `json:"attempt_id"`
	RetryEpoch       int64  `json:"retry_epoch"`
	SettingsRevision int64  `json:"settings_revision"`
	ConfirmRestart   bool   `json:"confirm_restart,omitempty"`
}

// SecretaryTaskStore is optional so task controls do not expand the general store contract.
type SecretaryTaskStore interface {
	ListSecretaryTasks(context.Context, string, SecretaryTaskQuery) (SecretaryTaskPage, error)
	GetSecretaryTask(
		context.Context,
		string,
		string,
		SecretaryTaskQuery,
	) (SecretaryTaskDetail, error)
	ControlSecretaryTask(
		context.Context,
		string,
		string,
		SecretaryTaskAction,
	) (SecretaryTask, error)
	ClaimSecretaryTaskRecovery(
		context.Context,
		string,
		string,
		SecretaryTaskAction,
		time.Duration,
	) (SecretaryJob, error)
	ReleaseSecretaryTaskRecovery(context.Context, SecretaryJob) error
}

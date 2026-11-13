package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	SecretaryMaxMessages              = 64
	SecretaryMaxBytes                 = 128 * 1024
	SecretaryMaxSourceBytes           = 8 * 1024 * 1024
	SecretaryLegacyInputPolicyVersion = "user-full-assistant-last-v1"
	SecretaryInputPolicyVersion       = "user-head-tail-assistant-last-v2"
	SecretaryInternalSource           = "secretary_internal"
	SecretaryInternalSessionPrefix    = "sess_secretary_"
	SecretaryMaxValidationRetries     = 2
)

var ErrSecretaryFenced = fmt.Errorf("secretary work canceled or superseded: %w", ErrConflict)

func SecretarySummaryID(owner, source string) string {
	return "summary_secretary_session_" + strings.TrimPrefix(SearchContentHash(owner+"\x00"+source), "sha256:")[:32]
}

type SecretarySettings struct {
	OwnerUserID string    `json:"owner_user_id"`
	AgentID     string    `json:"agent_id"`
	Enabled     bool      `json:"enabled"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SecretarySettingsUpdate struct {
	AgentID          string `json:"agent_id"`
	Enabled          bool   `json:"enabled"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type SecretaryStatus struct {
	SecretarySettings
	ExecutionState  string     `json:"execution_state,omitempty"`
	BlockedReason   string     `json:"blocked_reason,omitempty"`
	PendingCount    int        `json:"pending_count"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	Processing      bool       `json:"processing"`
}

type SecretarySourceMetadata struct {
	Title            string   `json:"title,omitempty"`
	PrimaryProjectID string   `json:"primary_project_id,omitempty"`
	CWD              string   `json:"cwd,omitempty"`
	WorkspaceRoots   []string `json:"workspace_roots,omitempty"`
}

type SecretaryJob struct {
	InputPolicyVersion      string                        `json:",omitempty"`
	OutputPolicyVersion     string                        `json:",omitempty"`
	OperationID             string                        `json:",omitempty"`
	Instructions            *SecretaryInstructionSnapshot `json:",omitempty"`
	BatchID                 string
	AttemptID               string
	RetryEpoch              int64
	Attempt                 *SecretaryAttempt
	CurrentSummaries        []SummaryVersion
	SummariesCursor         string
	SummariesHasMore        bool
	InvalidatedSummaries    []SummaryRef
	InvalidationKey         string
	ValidationFeedback      string
	SourceID                string
	OwnerUserID             string
	SourceAgentID           string
	SourceSessionID         string
	AgentID                 string
	LeaseToken              string
	SettingsRevision        int64
	RetryGeneration         int64
	ThroughRevision         int64
	RunID                   string
	TargetSummaryID         string
	ExpectedRevision        int64
	Messages                []SearchDocument
	PreviousSummaries       []SummaryVersion
	CurrentSummary          *SummaryVersion
	RemovedMessageIDs       []string
	ResetRequired           bool
	ResetReason             string
	SourceMetadata          SecretarySourceMetadata
	AllowedRelatedMessages  []MessageVersionRef
	AllowedRelatedSummaries []SummaryRef
}

type SecretaryStore interface {
	GetSecretarySettings(context.Context, string) (SecretarySettings, error)
	UpdateSecretarySettings(
		context.Context,
		string,
		SecretarySettingsUpdate,
	) (SecretarySettings, error)
	GetSecretaryStatus(context.Context, string) (SecretaryStatus, error)
	RetrySecretaryJobs(context.Context, string) error
	BackfillSecretaryJobs(context.Context) error
	ClaimSecretaryJobs(context.Context, int, time.Duration, time.Duration) ([]SecretaryJob, error)
	RegisterSecretaryInternalSession(context.Context, SecretaryJob, string, string) error
	SecretaryJobActive(context.Context, SecretaryJob) (bool, error)
	PublishSecretaryJob(
		context.Context,
		SecretaryJob,
		PublishSummaryRequest,
	) (SummaryVersion, error)
	FailSecretaryJob(context.Context, SecretaryJob, error) error
}

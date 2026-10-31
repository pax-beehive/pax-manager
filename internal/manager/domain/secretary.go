package domain

import (
	"context"
	"fmt"
	"time"
)

const (
	SecretaryMaxMessages           = 64
	SecretaryMaxBytes              = 128 * 1024
	SecretaryInternalSource        = "secretary_internal"
	SecretaryInternalSessionPrefix = "sess_secretary_"
)

var ErrSecretaryFenced = fmt.Errorf("secretary work canceled or superseded: %w", ErrConflict)

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
	PendingCount    int        `json:"pending_count"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	Processing      bool       `json:"processing"`
}

type SecretaryJob struct {
	SourceID          string
	OwnerUserID       string
	SourceAgentID     string
	SourceSessionID   string
	AgentID           string
	LeaseToken        string
	SettingsRevision  int64
	RetryGeneration   int64
	ThroughRevision   int64
	RunID             string
	TargetSummaryID   string
	ExpectedRevision  int64
	Messages          []SearchDocument
	PreviousSummaries []SummaryVersion
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

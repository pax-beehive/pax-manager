package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrSecretaryExecutionUnknown = errors.New("secretary execution outcome is unknown")
	// ErrSecretaryExecutionFailed requires a verified unsuccessful execution terminal.
	ErrSecretaryExecutionFailed  = errors.New("secretary execution ended without success")
	ErrSecretaryExecutionBlocked = errors.New("secretary execution requires an explicit retry")
	ErrSecretarySourceIncomplete = errors.New("secretary source turn is not complete")
	ErrSecretaryNotDispatched    = errors.New("secretary request was not dispatched")
	ErrSecretaryRetryDeferred    = errors.New("secretary connection retry is not due")
)

type SecretaryAttempt struct {
	ID            string             `json:"id"`
	BatchID       string             `json:"batch_id"`
	State         string             `json:"state"`
	Output        string             `json:"output,omitempty"`
	Reads         SecretaryToolReads `json:"reads"`
	Error         string             `json:"error,omitempty"`
	NotDispatched bool               `json:"not_dispatched,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type SecretaryExecutionStore interface {
	PrepareSecretaryBatch(context.Context, SecretaryJob) (SecretaryJob, error)
	BeginSecretaryAttempt(context.Context, SecretaryJob) (SecretaryAttempt, error)
	SaveSecretaryAttemptOutput(
		context.Context,
		SecretaryJob,
		string,
		string,
		SecretaryToolReads,
	) error
	FailSecretaryAttempt(context.Context, SecretaryJob, string, error) error
}

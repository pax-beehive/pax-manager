package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrRuntimeConfigVersionConflict reports a stale expected_version. The
// conflicting write is rolled back in full.
var ErrRuntimeConfigVersionConflict = errors.New("runtime config version conflict")

// RuntimeConfigConflictError carries the current version of the key.
type RuntimeConfigConflictError struct{ Current int64 }

func (e *RuntimeConfigConflictError) Error() string { return ErrRuntimeConfigVersionConflict.Error() }

func (e *RuntimeConfigConflictError) Is(target error) bool {
	return target == ErrRuntimeConfigVersionConflict
}

// ErrRuntimeConfigAuditNotFound reports an unknown audit id.
var ErrRuntimeConfigAuditNotFound = errors.New("runtime config audit record not found")

// RuntimeConfigRow is one database override. Credentials are never stored.
type RuntimeConfigRow struct {
	Key       string
	ValueJSON json.RawMessage
	Version   int64
	UpdatedBy string
	UpdatedAt time.Time
}

// RuntimeConfigValue is the stored form of a value, or nothing when Present is
// false (the key has no override).
type RuntimeConfigValue struct {
	Present   bool
	ValueJSON json.RawMessage
}

// RuntimeConfigWrite describes one set, reset, or revert. New.Present=false
// deletes the override.
type RuntimeConfigWrite struct {
	Key             string
	Action          string
	ExpectedVersion int64
	New             RuntimeConfigValue
	NewDisplay      *string
	Actor           string
	Reason          string
	// DisplayOld derives the display of the current value inside the write
	// transaction, so the audit never records a stale display.
	DisplayOld func(RuntimeConfigRow) *string
}

// RuntimeConfigWriteResult returns the row after the write (Version 0 and
// zero fields after a delete) and the new global revision.
type RuntimeConfigWriteResult struct {
	Row      RuntimeConfigRow
	Present  bool
	Revision int64
}

type RuntimeConfigAudit struct {
	ID         int64
	Key        string
	Action     string
	OldDisplay *string
	NewDisplay *string
	Old        RuntimeConfigValue
	Version    int64
	Actor      string
	Reason     string
	CreatedAt  time.Time
}

type RuntimeConfigAuditQuery struct {
	Key string
	// BeforeID pages backwards; zero starts at the newest record.
	BeforeID int64
	Limit    int
}

// RuntimeConfigRepository persists overrides, audit records, and the global
// revision. Every write is one transaction that checks the expected version,
// upserts or deletes, appends the audit record, and increments the revision.
type RuntimeConfigRepository interface {
	RuntimeConfigRevision(context.Context) (int64, error)
	LoadRuntimeConfig(context.Context) (int64, []RuntimeConfigRow, error)
	WriteRuntimeConfig(context.Context, RuntimeConfigWrite) (RuntimeConfigWriteResult, error)
	ListRuntimeConfigAudit(context.Context, RuntimeConfigAuditQuery) ([]RuntimeConfigAudit, error)
	GetRuntimeConfigAudit(context.Context, int64) (RuntimeConfigAudit, error)
}

package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Error codes shared with the API documentation.
const (
	CodeUnknownKey         = "unknown_key"
	CodeValidationFailed   = "validation_failed"
	CodeVersionConflict    = "version_conflict"
	CodeNotAdmin           = "not_admin"
	CodeFeatureUnavailable = "feature_unavailable"
)

const maxReasonRunes = 500

const testTimeout = 10 * time.Second

// Error is an admin API failure with a stable code.
type Error struct {
	Status      int
	Code        string
	Message     string
	FieldErrors map[string]string
	Version     *int64
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Data is the response data for this error.
func (e *Error) Data() map[string]any {
	data := map[string]any{"error": e.Code}
	if len(e.FieldErrors) > 0 {
		data["field_errors"] = e.FieldErrors
	}
	if e.Version != nil {
		data["version"] = *e.Version
	}
	return data
}

func validationError(field, message string) *Error {
	return &Error{
		Status: http.StatusBadRequest, Code: CodeValidationFailed, Message: "validation failed",
		FieldErrors: map[string]string{field: message},
	}
}

func unknownKey() *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeUnknownKey, Message: "unknown config key"}
}

var errNoRepository = &Error{
	Status: http.StatusServiceUnavailable, Code: "unavailable",
	Message: "runtime config storage is unavailable",
}

type EntryView struct {
	Key         string   `json:"key"`
	Kind        Kind     `json:"kind"`
	Group       string   `json:"group"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Options     []string `json:"options"`
	// Secret is always false: credentials are environment-only and never
	// appear in this API. The field is kept for the Console contract.
	Secret         bool       `json:"secret"`
	Value          any        `json:"value"`
	Display        *string    `json:"display"`
	HasValue       bool       `json:"has_value"`
	Source         string     `json:"source"`
	EnvVar         string     `json:"env_var"`
	DefaultDisplay *string    `json:"default_display"`
	Apply          Apply      `json:"apply"`
	PendingRestart bool       `json:"pending_restart"`
	Testable       bool       `json:"testable"`
	Version        int64      `json:"version"`
	UpdatedAt      *time.Time `json:"updated_at"`
	UpdatedBy      *string    `json:"updated_by"`
}

type AdminView struct {
	Region   string      `json:"region"`
	Revision int64       `json:"revision"`
	Health   Health      `json:"health"`
	Entries  []EntryView `json:"entries"`
}

// View returns every entry visible in this region.
func (s *Store) View() AdminView {
	snapshot := s.Snapshot()
	region := s.opts.Region
	if region == "" {
		region = "local"
	}
	view := AdminView{
		Region:   region,
		Revision: snapshot.Revision,
		Health:   s.Health(),
		Entries:  []EntryView{},
	}
	for i := range snapshot.entries {
		entry := &snapshot.entries[i]
		if !entry.visibleIn(s.opts.Region) {
			continue
		}
		view.Entries = append(view.Entries, s.entryView(snapshot, entry.Key))
	}
	return view
}

func (s *Store) entryView(snapshot *Resolved, key string) EntryView {
	value := snapshot.values[key]
	entry := value.Entry
	shown := display(entry, value.Value)
	view := EntryView{
		Key: entry.Key, Kind: entry.Kind, Group: entry.Group, Label: entry.Label,
		Description: entry.Description, Type: entry.Type, Options: entry.Options,
		Value: value.Value, DefaultDisplay: display(entry, entry.Default), Display: shown, HasValue: shown != nil, Source: value.Source,
		EnvVar: entry.EnvVar, Apply: entry.Apply, Testable: entry.Tester != nil,
		Version: value.Version,
	}
	if entry.Type == TypeFeature {
		view.HasValue = true
	}
	if value.Source == SourceDB {
		at := value.UpdatedAt
		by := value.UpdatedBy
		view.UpdatedAt, view.UpdatedBy = &at, &by
		view.PendingRestart = entry.Apply == ApplyRestart && value.UpdatedAt.After(s.startedAt)
	}
	return view
}

func (s *Store) lookup(key string) (*Entry, *Error) {
	snapshot := s.Snapshot()
	value, ok := snapshot.values[key]
	if !ok || !value.Entry.visibleIn(s.opts.Region) {
		return nil, unknownKey()
	}
	return value.Entry, nil
}

func checkReason(reason string) (string, *Error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", validationError("reason", "is required")
	}
	if utf8.RuneCountInString(reason) > maxReasonRunes {
		return "", validationError("reason", "must be at most 500 characters")
	}
	return reason, nil
}

// Set stores an override after validation.
func (s *Store) Set(
	ctx context.Context,
	key string,
	raw json.RawMessage,
	expectedVersion int64,
	reason, actor string,
) (EntryView, error) {
	entry, keyErr := s.lookup(key)
	if keyErr != nil {
		return EntryView{}, keyErr
	}
	if s.opts.Repository == nil {
		return EntryView{}, errNoRepository
	}
	reason, reasonErr := checkReason(reason)
	if reasonErr != nil {
		return EntryView{}, reasonErr
	}
	if expectedVersion < 0 {
		return EntryView{}, validationError("expected_version", "must not be negative")
	}
	value, err := decodeValue(entry, raw)
	if err != nil {
		return EntryView{}, validationError("value", err.Error())
	}
	encoded, err := encodeValue(value)
	if err != nil {
		return EntryView{}, err
	}
	stored := domain.RuntimeConfigValue{Present: true, ValueJSON: encoded}
	return s.write(ctx, entry, domain.RuntimeConfigWrite{
		Key: entry.Key, Action: "set", ExpectedVersion: expectedVersion, New: stored,
		NewDisplay: display(entry, value), Actor: actor, Reason: reason,
	})
}

// Reset deletes the override so the entry falls back to env or default.
func (s *Store) Reset(
	ctx context.Context,
	key string,
	expectedVersion int64,
	reason, actor string,
) (EntryView, error) {
	entry, keyErr := s.lookup(key)
	if keyErr != nil {
		return EntryView{}, keyErr
	}
	if s.opts.Repository == nil {
		return EntryView{}, errNoRepository
	}
	reason, reasonErr := checkReason(reason)
	if reasonErr != nil {
		return EntryView{}, reasonErr
	}
	fallback := baseValue(entry, s.opts.Env)
	return s.write(ctx, entry, domain.RuntimeConfigWrite{
		Key: entry.Key, Action: "reset", ExpectedVersion: expectedVersion,
		NewDisplay: display(entry, fallback.Value), Actor: actor, Reason: reason,
	})
}

// Revert restores the old value recorded by an audit entry.
func (s *Store) Revert(
	ctx context.Context,
	auditID int64,
	expectedVersion int64,
	reason, actor string,
) (EntryView, error) {
	if s.opts.Repository == nil {
		return EntryView{}, errNoRepository
	}
	record, err := s.opts.Repository.GetRuntimeConfigAudit(ctx, auditID)
	if errors.Is(err, domain.ErrRuntimeConfigAuditNotFound) {
		return EntryView{}, &Error{
			Status: http.StatusNotFound, Code: "not_found", Message: "audit record not found",
		}
	}
	if err != nil {
		return EntryView{}, err
	}
	entry, keyErr := s.lookup(record.Key)
	if keyErr != nil {
		return EntryView{}, keyErr
	}
	reason, reasonErr := checkReason(reason)
	if reasonErr != nil {
		return EntryView{}, reasonErr
	}
	restored := record.Old
	newDisplay := record.OldDisplay
	switch {
	case !restored.Present:
		newDisplay = display(entry, baseValue(entry, s.opts.Env).Value)
	default:
		if _, decodeErr := decodeValue(entry, restored.ValueJSON); decodeErr != nil {
			return EntryView{}, validationError("audit", "the recorded value is no longer valid")
		}
	}
	return s.write(ctx, entry, domain.RuntimeConfigWrite{
		Key: entry.Key, Action: "revert", ExpectedVersion: expectedVersion, New: restored,
		NewDisplay: newDisplay, Actor: actor, Reason: reason,
	})
}

func (s *Store) write(
	ctx context.Context,
	entry *Entry,
	request domain.RuntimeConfigWrite,
) (EntryView, error) {
	request.DisplayOld = func(row domain.RuntimeConfigRow) *string {
		value, err := rowValue(entry, row)
		if err != nil {
			return nil
		}
		return display(entry, value)
	}
	_, err := s.opts.Repository.WriteRuntimeConfig(ctx, request)
	var conflict *domain.RuntimeConfigConflictError
	if errors.As(err, &conflict) {
		current := conflict.Current
		return EntryView{}, &Error{
			Status: http.StatusConflict, Code: CodeVersionConflict,
			Message: "config was changed by someone else", Version: &current,
		}
	}
	if err != nil {
		return EntryView{}, err
	}
	// Apply the write on this instance immediately; other instances poll.
	_ = s.Refresh(ctx)
	return s.entryView(s.Snapshot(), entry.Key), nil
}

type AuditItem struct {
	ID         int64     `json:"id"`
	Key        string    `json:"key"`
	Action     string    `json:"action"`
	OldDisplay *string   `json:"old_display"`
	NewDisplay *string   `json:"new_display"`
	Version    int64     `json:"version"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
	Revertible bool      `json:"revertible"`
}

type AuditPage struct {
	Items      []AuditItem `json:"items"`
	NextCursor *string     `json:"next_cursor"`
}

// Audit lists records newest first.
func (s *Store) Audit(ctx context.Context, key, cursor string, limit int) (AuditPage, error) {
	if s.opts.Repository == nil {
		return AuditPage{}, errNoRepository
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		return AuditPage{}, validationError("limit", "must be at most 200")
	}
	var before int64
	if cursor != "" {
		parsed, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || parsed <= 0 {
			return AuditPage{}, validationError("cursor", "is invalid")
		}
		before = parsed
	}
	records, err := s.opts.Repository.ListRuntimeConfigAudit(ctx, domain.RuntimeConfigAuditQuery{
		Key: strings.TrimSpace(key), BeforeID: before, Limit: limit,
	})
	if err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Items: make([]AuditItem, 0, len(records))}
	for _, record := range records {
		page.Items = append(page.Items, AuditItem{
			ID: record.ID, Key: record.Key, Action: record.Action,
			OldDisplay: record.OldDisplay, NewDisplay: record.NewDisplay,
			Version: record.Version, Actor: record.Actor, Reason: record.Reason,
			CreatedAt: record.CreatedAt, Revertible: s.revertible(record),
		})
	}
	if len(records) == limit {
		next := strconv.FormatInt(records[len(records)-1].ID, 10)
		page.NextCursor = &next
	}
	return page, nil
}

func (s *Store) revertible(record domain.RuntimeConfigAudit) bool {
	entry, err := s.lookup(record.Key)
	if err != nil {
		return false
	}
	if !record.Old.Present {
		return true
	}
	_, decodeErr := decodeValue(entry, record.Old.ValueJSON)
	return decodeErr == nil
}

// Test runs a group's tester against the effective values plus unsaved
// overrides. It never writes. Credentials always come from the environment.
func (s *Store) Test(
	ctx context.Context,
	group string,
	overrides map[string]json.RawMessage,
) (TestResult, error) {
	snapshot := s.Snapshot()
	values := ResolvedGroup{}
	var tester func(context.Context, ResolvedGroup) TestResult
	for i := range snapshot.entries {
		entry := &snapshot.entries[i]
		if entry.Group != group || !entry.visibleIn(s.opts.Region) {
			continue
		}
		values[entry.Key] = snapshot.values[entry.Key].Value
		if entry.Tester != nil {
			tester = entry.Tester
		}
	}
	if tester == nil {
		return TestResult{}, validationError("group", "has no connection test")
	}
	// Testers use the environment credentials; they cannot be overridden.
	for name, value := range snapshot.credentials {
		values[name] = value
	}
	for key, raw := range overrides {
		entry, keyErr := s.lookup(key)
		if keyErr != nil {
			return TestResult{}, keyErr
		}
		if entry.Group != group {
			return TestResult{}, validationError(key, "belongs to another group")
		}
		value, err := decodeValue(entry, raw)
		if err != nil {
			return TestResult{}, validationError(key, err.Error())
		}
		values[key] = value
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	started := time.Now()
	result := tester(ctx, values)
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

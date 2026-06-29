package domain

import (
	"encoding/json"
	"time"
)

const (
	TeamMemexDocumentStatusActive   = "active"
	TeamMemexDocumentStatusArchived = "archived"

	TeamMemexRunExecutorDryRun = "dry_run"

	TeamMemexRunStatusPending          = "pending"
	TeamMemexRunStatusRunning          = "running"
	TeamMemexRunStatusSucceeded        = "succeeded"
	TeamMemexRunStatusPartial          = "partial"
	TeamMemexRunStatusProviderFailed   = "provider_failed"
	TeamMemexRunStatusValidationFailed = "validation_failed"
	TeamMemexRunStatusFailed           = "failed"
)

type TeamMemexDocument struct {
	DocumentID string          `json:"document_id"`
	TeamID     string          `json:"team_id"`
	Path       string          `json:"path"`
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	Tags       json.RawMessage `json:"tags,omitempty"`
	BodyMD     string          `json:"body_md"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	ArchivedAt *time.Time      `json:"archived_at,omitempty"`
}

type TeamMemexRunConstraints struct {
	MaxInputMessages       int      `json:"max_input_messages"`
	MaxInputMessageChars   int      `json:"max_input_message_chars"`
	MaxDocsReadPerRun      int      `json:"max_docs_read_per_run"`
	MaxDocCharsReadPerRun  int      `json:"max_doc_chars_read_per_run"`
	MaxOutputDocs          int      `json:"max_output_docs"`
	MaxOutputDocCharsEach  int      `json:"max_output_doc_chars_each"`
	AllowedOperations      []string `json:"allowed_operations"`
	IndexIsReadOnly        bool     `json:"index_is_read_only"`
	EmbeddingEnabled       bool     `json:"embedding_enabled"`
	MaxRepairAttempts      int      `json:"max_repair_attempts"`
	InitialExecutorEnabled bool     `json:"initial_executor_enabled"`
}

type TeamMemexValidationError struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type TeamMemexValidationReport struct {
	Retryable   bool                       `json:"retryable"`
	Errors      []TeamMemexValidationError `json:"errors"`
	Constraints TeamMemexRunConstraints    `json:"constraints"`
}

type TeamMemexRun struct {
	RunID             string                     `json:"run_id"`
	TeamID            string                     `json:"team_id"`
	RequestedByUserID string                     `json:"requested_by_user_id"`
	ExecutorType      string                     `json:"executor_type"`
	Status            string                     `json:"status"`
	Partial           bool                       `json:"partial"`
	Constraints       TeamMemexRunConstraints    `json:"constraints"`
	IndexMD           string                     `json:"index_md"`
	ValidationReport  *TeamMemexValidationReport `json:"validation_report,omitempty"`
	Error             string                     `json:"error,omitempty"`
	StartedAt         time.Time                  `json:"started_at"`
	CompletedAt       *time.Time                 `json:"completed_at,omitempty"`
}

func DefaultTeamMemexRunConstraints() TeamMemexRunConstraints {
	return TeamMemexRunConstraints{
		MaxInputMessages:       200,
		MaxInputMessageChars:   120000,
		MaxDocsReadPerRun:      20,
		MaxDocCharsReadPerRun:  160000,
		MaxOutputDocs:          20,
		MaxOutputDocCharsEach:  64000,
		AllowedOperations:      []string{"create_doc", "update_doc", "archive_doc", "no_op"},
		IndexIsReadOnly:        true,
		EmbeddingEnabled:       false,
		MaxRepairAttempts:      2,
		InitialExecutorEnabled: false,
	}
}

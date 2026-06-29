package domain

import (
	"encoding/json"
	"time"
)

const (
	TeamMemexDocumentStatusActive   = "active"
	TeamMemexDocumentStatusArchived = "archived"
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

package domain

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrSummaryVersionMismatch   = errors.New("summary version mismatch")
	ErrSourceVersionUnavailable = errors.New("source version unavailable")
)

// MessageVersionRef identifies a complete canonical visible message. Receipts
// prove what a caller read; they are not part of the persistent identity.
type MessageVersionRef struct {
	MessageID               string `json:"message_id"`
	ContentHash             string `json:"content_hash"`
	CanonicalizationVersion string `json:"canonicalization_version"`
	ReadReceipt             string `json:"read_receipt,omitempty"`
}

type SummaryRef struct {
	SummaryID string `json:"summary_id"`
	Revision  int64  `json:"revision"`
}

type SummaryEvidence struct {
	Message     MessageVersionRef `json:"message"`
	Quote       string            `json:"quote"`
	View        string            `json:"view,omitempty"`
	ReadReceipt string            `json:"read_receipt,omitempty"`
}

type SummaryClaim struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind,omitempty"`
	Text       string            `json:"text"`
	Basis      string            `json:"basis,omitempty"`
	State      string            `json:"state,omitempty"`
	Supersedes []string          `json:"supersedes,omitempty"`
	Evidence   []SummaryEvidence `json:"evidence,omitempty"`
}

type PublishSummaryRequest struct {
	SummaryID        string              `json:"summary_id"`
	SubjectType      string              `json:"subject_type"`
	SubjectID        string              `json:"subject_id"`
	ExpectedRevision int64               `json:"expected_revision"`
	RequestID        string              `json:"request_id"`
	Title            string              `json:"title"`
	Body             string              `json:"body"`
	Claims           []SummaryClaim      `json:"claims,omitempty"`
	Coverage         []MessageVersionRef `json:"coverage"`
	SummarySources   []SummaryRef        `json:"summary_sources,omitempty"`
}

type SummaryVersion struct {
	SummaryID      string              `json:"summary_id"`
	Revision       int64               `json:"revision"`
	OwnerUserID    string              `json:"owner_user_id"`
	SubjectType    string              `json:"subject_type"`
	SubjectID      string              `json:"subject_id"`
	Title          string              `json:"title"`
	Body           string              `json:"body"`
	Claims         []SummaryClaim      `json:"claims,omitempty"`
	Coverage       []MessageVersionRef `json:"coverage"`
	SummarySources []SummaryRef        `json:"summary_sources,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
}

type SummaryStore interface {
	PublishSummary(context.Context, string, PublishSummaryRequest) (SummaryVersion, error)
	GetSummary(context.Context, string, string, int64) (SummaryVersion, error)
	ReadMessageVersion(context.Context, string, MessageVersionRef) (SearchDocument, error)
}

func (r PublishSummaryRequest) Validate() error {
	if strings.TrimSpace(r.SummaryID) == "" || strings.TrimSpace(r.SubjectID) == "" ||
		strings.TrimSpace(
			r.RequestID,
		) == "" || strings.TrimSpace(r.Body) == "" || r.ExpectedRevision < 0 ||
		len(
			r.Body,
		) > 1024*1024 || len(r.Title) > 4096 || len(r.Coverage)+len(r.SummarySources) == 0 ||
		len(r.Coverage)+len(r.SummarySources) > 512 || len(r.Claims) > 512 {
		return ErrConflict
	}
	switch r.SubjectType {
	case "fragment", "topic", "direction", "project":
	default:
		return ErrConflict
	}
	return r.validateSourcesAndClaims()
}

func (r PublishSummaryRequest) validateSourcesAndClaims() error {
	covered := make(map[string]bool, len(r.Coverage))
	for _, ref := range r.Coverage {
		key := ref.Identity()
		if !ref.Valid() || covered[key] {
			return ErrConflict
		}
		covered[key] = true
	}
	children := make(map[SummaryRef]bool, len(r.SummarySources))
	for _, ref := range r.SummarySources {
		if ref.SummaryID == "" || ref.Revision <= 0 || children[ref] {
			return ErrConflict
		}
		children[ref] = true
	}
	claims := make(map[string]bool, len(r.Claims))
	for _, claim := range r.Claims {
		if claim.ID == "" || strings.TrimSpace(claim.Text) == "" || claims[claim.ID] {
			return ErrConflict
		}
		claims[claim.ID] = true
		for _, evidence := range claim.Evidence {
			if !covered[evidence.Message.Identity()] || evidence.Quote == "" {
				return ErrConflict
			}
		}
	}
	return nil
}

func (r MessageVersionRef) Valid() bool {
	return r.MessageID != "" && r.ContentHash != "" && r.CanonicalizationVersion != ""
}

func (r MessageVersionRef) Identity() string {
	return r.MessageID + "\x00" + r.CanonicalizationVersion + "\x00" + r.ContentHash
}

package domain

import (
	"context"
	"time"
)

type SummaryListQuery struct {
	Cursor string
	Limit  int
}

type SummaryListItem struct {
	NodeType           string    `json:"node_type,omitempty"`
	NodeStatus         string    `json:"node_status,omitempty"`
	OriginSourceID     string    `json:"origin_source_id,omitempty"`
	OriginAgentID      string    `json:"origin_agent_id,omitempty"`
	OriginSessionID    string    `json:"origin_session_id,omitempty"`
	OriginSessionTitle string    `json:"origin_session_title,omitempty"`
	SummaryID          string    `json:"summary_id"`
	Revision           int64     `json:"revision"`
	SubjectType        string    `json:"subject_type"`
	SubjectID          string    `json:"subject_id"`
	Title              string    `json:"title"`
	BodyPreview        string    `json:"body_preview"`
	ClaimCount         int       `json:"claim_count"`
	CoverageCount      int       `json:"coverage_count"`
	SummarySourceCount int       `json:"summary_source_count"`
	CreatedAt          time.Time `json:"created_at"`
}

type SummaryListPage struct {
	Items      []SummaryListItem `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type SummaryBrowserStore interface {
	ListCurrentSummaries(context.Context, string, SummaryListQuery) (SummaryListPage, error)
}

type SummaryRecentBrowserStore interface {
	ListRecentSummaries(context.Context, string, SummaryListQuery) (SummaryListPage, error)
}

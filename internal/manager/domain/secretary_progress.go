package domain

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidCursor = errors.New("invalid cursor")

type SecretaryProgressQuery struct {
	Cursor string
	Limit  int
}

type SecretaryCoverageCounts struct {
	TotalMessages     int `json:"total_messages"`
	CompletedMessages int `json:"completed_messages"`
	PendingMessages   int `json:"pending_messages"`
	TotalTurns        int `json:"total_turns"`
	CompletedTurns    int `json:"completed_turns"`
	PendingTurns      int `json:"pending_turns"`
	UngroupedMessages int `json:"ungrouped_messages"`
}

type SecretaryProgressCounts struct {
	SecretaryCoverageCounts
	TotalSessions     int `json:"total_sessions"`
	CompletedSessions int `json:"completed_sessions"`
	PartialSessions   int `json:"partial_sessions"`
	UnstartedSessions int `json:"unstarted_sessions"`
}

type SecretarySessionProgress struct {
	SecretaryCoverageCounts
	SourceID        string     `json:"source_id"`
	AgentID         string     `json:"agent_id"`
	SessionID       string     `json:"session_id"`
	Title           string     `json:"title"`
	State           string     `json:"state"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
}

type SecretaryProgressPage struct {
	AsOf       time.Time                  `json:"as_of"`
	Counts     SecretaryProgressCounts    `json:"counts"`
	Sessions   []SecretarySessionProgress `json:"sessions"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

// Progress measures current messages covered by successfully published summaries.
type SecretaryProgressStore interface {
	ReadSecretaryProgress(
		context.Context,
		string,
		SecretaryProgressQuery,
	) (SecretaryProgressPage, error)
}

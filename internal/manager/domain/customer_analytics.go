package domain

import (
	"context"
	"time"
)

// CustomerAnalytics contains metadata only; encrypted history is not decrypted.
type CustomerAnalytics struct {
	UserID           string     `json:"user_id"`
	Email            string     `json:"email"`
	IsAdmin          bool       `json:"is_admin"          gorm:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	FirstVisitAt     *time.Time `json:"first_visit_at"`
	LastVisitAt      *time.Time `json:"last_visit_at"`
	Devices          int64      `json:"devices"`
	Agents           int64      `json:"agents"`
	FirstBoundAt     *time.Time `json:"first_bound_at"`
	UserMessages     int64      `json:"user_messages"`
	FirstMessageAt   *time.Time `json:"first_message_at"`
	LastMessageAt    *time.Time `json:"last_message_at"`
	EncryptedRecords int64      `json:"encrypted_records"`
	Sessions         int64      `json:"sessions"`
}

type CustomerAnalyticsStore interface {
	CustomerAnalytics(context.Context) ([]CustomerAnalytics, error)
	RecordCustomerVisit(context.Context, string, time.Time) error
}

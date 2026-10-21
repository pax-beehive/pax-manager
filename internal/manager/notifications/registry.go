package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidRegistration = errors.New("invalid notification registration")
	ErrReceiverConflict    = errors.New("receiver belongs to another account")
	ErrReceiverNotFound    = errors.New("receiver not found")
	ErrReceiverLimit       = errors.New("receiver limit reached")
	ErrInvalidFilter       = errors.New("invalid subscription filter")
)

const MaxReceiversPerUser = 20

type SubscriptionStatus string

const (
	SubscriptionEnabled     SubscriptionStatus = "enabled"
	SubscriptionDisabled    SubscriptionStatus = "disabled"
	SubscriptionRevoked     SubscriptionStatus = "revoked"
	SubscriptionExpired     SubscriptionStatus = "expired"
	SubscriptionInvalidated SubscriptionStatus = "invalidated"
)

type Registration struct {
	Subscription WebPushSubscription `json:"subscription"`
	DeviceLabel  string              `json:"device_label"`
	VAPIDKeyID   string              `json:"vapid_key_id"`
	Permission   string              `json:"permission"`
	Enabled      bool                `json:"enabled"`
	ExpiresAt    *time.Time          `json:"expires_at,omitempty"`
}

// RegistrationRecord is private persistence data and must never be an API DTO.
type RegistrationRecord struct {
	Receiver
	DeviceLabel          string
	VAPIDKeyID           string
	Permission           string
	PermissionReportedAt time.Time
	CreatedAt            time.Time
	LastRegisteredAt     time.Time
	RevokedAt            *time.Time
	InvalidatedAt        *time.Time
}

type DeliveryMetadata struct {
	Status         string     `json:"status"`
	AttemptedAt    time.Time  `json:"attempted_at"`
	Attempts       int        `json:"attempts"`
	ProviderStatus int        `json:"provider_status,omitempty"`
	ErrorCategory  string     `json:"error_category,omitempty"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
}

type ReceiverMetadata struct {
	ID                   string             `json:"receiver_id"`
	UserID               string             `json:"user_id"`
	Email                string             `json:"email"`
	DeviceLabel          string             `json:"device_label"`
	Channel              Channel            `json:"channel"`
	Revision             int64              `json:"revision"`
	Status               SubscriptionStatus `json:"status"`
	Permission           string             `json:"permission"`
	PermissionReportedAt time.Time          `json:"permission_reported_at"`
	CreatedAt            time.Time          `json:"created_at"`
	LastRegisteredAt     time.Time          `json:"last_registered_at"`
	ExpiresAt            *time.Time         `json:"expires_at,omitempty"`
	LatestDelivery       *DeliveryMetadata  `json:"latest_delivery"`
}

func (r RegistrationRecord) Metadata(email string, now time.Time) ReceiverMetadata {
	status := SubscriptionEnabled
	switch {
	case r.RevokedAt != nil:
		status = SubscriptionRevoked
	case r.InvalidatedAt != nil:
		status = SubscriptionInvalidated
	case r.ExpiresAt != nil && !now.Before(*r.ExpiresAt):
		status = SubscriptionExpired
	case !r.Enabled:
		status = SubscriptionDisabled
	}
	permission := r.Permission
	if permission == "" {
		permission = "unknown"
	}
	return ReceiverMetadata{
		ID: r.ID, UserID: r.UserID, Email: email, DeviceLabel: r.DeviceLabel,
		Channel: r.Channel, Revision: r.Revision, Status: status, Permission: permission,
		PermissionReportedAt: r.PermissionReportedAt, CreatedAt: r.CreatedAt,
		LastRegisteredAt: r.LastRegisteredAt, ExpiresAt: r.ExpiresAt,
	}
}

func ReceiverID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return "nrecv_" + hex.EncodeToString(sum[:])
}

func RefreshRegistration(
	old RegistrationRecord,
	owner string,
	input Registration,
	now time.Time,
) (RegistrationRecord, error) {
	input, err := normalizeRegistration(input, now)
	if err != nil || owner == "" {
		return RegistrationRecord{}, ErrInvalidRegistration
	}
	if old.ID != "" && old.UserID != owner {
		return RegistrationRecord{}, ErrReceiverConflict
	}
	if old.ID == "" {
		old.Receiver = Receiver{
			ID:      ReceiverID(input.Subscription.Endpoint),
			UserID:  owner,
			Channel: ChannelWebPush,
		}
		old.CreatedAt = now.UTC()
	}
	if old.Revision == 0 || old.Subscription != input.Subscription ||
		old.Enabled != input.Enabled ||
		old.VAPIDKeyID != input.VAPIDKeyID ||
		!equalTime(old.ExpiresAt, input.ExpiresAt) ||
		old.RevokedAt != nil ||
		old.InvalidatedAt != nil {
		old.Revision++
	}
	old.Subscription, old.Enabled, old.ExpiresAt = input.Subscription, input.Enabled, input.ExpiresAt
	old.DeviceLabel, old.VAPIDKeyID, old.Permission = strings.TrimSpace(
		input.DeviceLabel,
	), input.VAPIDKeyID, input.Permission
	old.PermissionReportedAt, old.LastRegisteredAt = now.UTC(), now.UTC()
	old.RevokedAt, old.InvalidatedAt = nil, nil
	return old, nil
}

func normalizeRegistration(input Registration, now time.Time) (Registration, error) {
	if input.Subscription.ExpirationTime != nil {
		expiresAt := time.UnixMilli(*input.Subscription.ExpirationTime).UTC()
		if expiresAt.Year() < 1 || expiresAt.Year() > 9999 {
			return Registration{}, ErrInvalidRegistration
		}
		if input.ExpiresAt == nil || expiresAt.Before(*input.ExpiresAt) {
			input.ExpiresAt = &expiresAt
		}
		input.Subscription.ExpirationTime = nil
	}
	if input.ExpiresAt != nil && (input.ExpiresAt.Year() < 1 || input.ExpiresAt.Year() > 9999) {
		return Registration{}, ErrInvalidRegistration
	}
	if len(input.DeviceLabel) > 120 || input.VAPIDKeyID == "" ||
		len(input.VAPIDKeyID) > 128 ||
		input.Subscription.Validate() != nil ||
		(input.ExpiresAt != nil && !now.Before(*input.ExpiresAt)) {
		return Registration{}, ErrInvalidRegistration
	}
	switch input.Permission {
	case "granted", "denied", "default", "unsupported", "unknown":
	default:
		return Registration{}, ErrInvalidRegistration
	}
	return input, nil
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

type SubscriptionFilter struct {
	Search  string
	UserID  string
	Status  SubscriptionStatus
	Channel Channel
	After   string
	Limit   int
}

func (f SubscriptionFilter) Validate() error {
	if f.Limit < 0 || f.Limit > 100 || len(f.Search) > 200 || len(f.UserID) > 512 ||
		len(f.After) > 80 ||
		(f.Channel != "" && f.Channel != ChannelWebPush) {
		return ErrInvalidFilter
	}
	switch f.Status {
	case "",
		SubscriptionEnabled,
		SubscriptionDisabled,
		SubscriptionRevoked,
		SubscriptionExpired,
		SubscriptionInvalidated:
		return nil
	default:
		return ErrInvalidFilter
	}
}

type SubscriptionSummary struct {
	Total        int64 `json:"total"`
	Enabled      int64 `json:"enabled"`
	Disabled     int64 `json:"disabled"`
	Revoked      int64 `json:"revoked"`
	Expired      int64 `json:"expired"`
	Invalidated  int64 `json:"invalidated"`
	EnabledUsers int64 `json:"enabled_users"`
}

type SubscriptionPage struct {
	Receivers  []ReceiverMetadata  `json:"receivers"`
	Summary    SubscriptionSummary `json:"summary"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type Registry interface {
	RegisterNotificationReceiver(context.Context, string, Registration) (ReceiverMetadata, error)
	RevokeNotificationReceiver(context.Context, string, string) error
	ListNotificationSubscriptions(context.Context, SubscriptionFilter) (SubscriptionPage, error)
}

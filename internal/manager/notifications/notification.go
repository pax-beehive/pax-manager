// Package notifications owns channel-independent turn notification and delivery rules.
package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const NotificationTTL = time.Hour

var ErrInvalidCompletion = errors.New("invalid turn completion")

// TurnCompletion must come from an authenticated, durably recorded terminal turn.
type TurnCompletion struct {
	UserID       string
	NodeID       string
	AgentID      string
	SessionID    string
	TurnID       string
	CompletedAt  time.Time
	ReplyPreview string
}

type Payload struct {
	Version        int    `json:"version"`
	NotificationID string `json:"notification_id"`
	Type           string `json:"type"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	URL            string `json:"url"`
	Tag            string `json:"tag"`
}

type Notification struct {
	ID        string
	UserID    string
	ExpiresAt time.Time
	Payload   Payload
}

func BuildTurnNotification(event TurnCompletion) (Notification, error) {
	for _, id := range []string{
		event.UserID, event.NodeID, event.AgentID, event.SessionID, event.TurnID,
	} {
		if id == "" || len(id) > 512 || strings.TrimSpace(id) != id {
			return Notification{}, ErrInvalidCompletion
		}
	}
	if event.CompletedAt.IsZero() {
		return Notification{}, ErrInvalidCompletion
	}
	id := stableID("ntf_", event.UserID, event.AgentID, event.SessionID, event.TurnID)
	query := url.Values{"agentId": {event.AgentID}, "nodeId": {event.NodeID}}
	return Notification{
		ID: id, UserID: event.UserID,
		ExpiresAt: event.CompletedAt.UTC().Add(NotificationTTL),
		Payload: Payload{
			Version: 1, NotificationID: id, Type: "turn.completed",
			Title: "Pax Workspace", Body: turnNotificationBody(event.ReplyPreview),
			URL: "/sessions/" + url.PathEscape(event.SessionID) + "?" + query.Encode(),
			Tag: id,
		},
	}, nil
}

func turnNotificationBody(text string) string {
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if text == "" {
		return "Your turn has finished."
	}
	runes := []rune(text)
	if len(runes) > 200 {
		return string(runes[:199]) + "\u2026"
	}
	return text
}

type Delivery struct {
	ID               string
	NotificationID   string
	UserID           string
	ReceiverID       string
	ReceiverRevision int64
	Channel          Channel
	State            DeliveryState
	NextAttemptAt    time.Time
	ExpiresAt        time.Time
}

// PlanDeliveries fans out once per receiver. Storage must also enforce unique IDs.
func PlanDeliveries(notification Notification, receivers []Receiver, now time.Time) []Delivery {
	deliveries := make([]Delivery, 0)
	if notification.ID == "" || notification.UserID == "" || !now.Before(notification.ExpiresAt) {
		return deliveries
	}
	seen := make(map[string]bool, len(receivers))
	for _, receiver := range receivers {
		if receiver.UserID != notification.UserID || !receiver.canReceive(now) ||
			seen[receiver.ID] {
			continue
		}
		seen[receiver.ID] = true
		deliveries = append(deliveries, Delivery{
			ID:             stableID("ndlv_", notification.ID, receiver.ID),
			NotificationID: notification.ID, UserID: notification.UserID,
			ReceiverID: receiver.ID, ReceiverRevision: receiver.Revision,
			Channel: receiver.Channel, State: StatePending,
			NextAttemptAt: now.UTC(), ExpiresAt: notification.ExpiresAt,
		})
	}
	return deliveries
}

// CanSendTo rechecks persisted ownership and revision immediately before sending.
func (d Delivery) CanSendTo(receiver Receiver, now time.Time) bool {
	return d.State == StatePending && !now.Before(d.NextAttemptAt) && now.Before(d.ExpiresAt) &&
		d.UserID == receiver.UserID && d.ReceiverID == receiver.ID &&
		d.ReceiverRevision == receiver.Revision && d.Channel == receiver.Channel &&
		receiver.canReceive(now)
}

func stableID(prefix string, parts ...string) string {
	// JSON preserves component boundaries even if opaque IDs contain separators.
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(encoded)
	return prefix + hex.EncodeToString(sum[:])
}

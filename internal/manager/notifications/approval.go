package notifications

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrInvalidApproval = errors.New("invalid approval request")

type ApprovalRequest struct {
	UserID     string
	NodeID     string
	AgentID    string
	SessionID  string
	ApprovalID string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

func BuildApprovalNotification(event ApprovalRequest) (Notification, error) {
	for _, id := range []string{event.UserID, event.NodeID, event.AgentID, event.SessionID, event.ApprovalID} {
		if id == "" || len(id) > 512 || strings.TrimSpace(id) != id {
			return Notification{}, ErrInvalidApproval
		}
	}
	if event.CreatedAt.IsZero() {
		return Notification{}, ErrInvalidApproval
	}
	id := stableID("ntf_", event.UserID, "approval.required", event.ApprovalID)
	expires := event.CreatedAt.UTC().Add(NotificationTTL)
	if event.ExpiresAt != nil && event.ExpiresAt.Before(expires) {
		expires = event.ExpiresAt.UTC()
	}
	return Notification{
		ID: id, UserID: event.UserID, ExpiresAt: expires,
		Payload: Payload{
			Version: 1, NotificationID: id, Type: "approval.required", ApprovalID: event.ApprovalID,
			Title: "Approval needed",
			Body:  "Your session is waiting for your approval. Tap to review the request.",
			URL:   "/?" + url.Values{"session_id": {event.SessionID}}.Encode(), Tag: id,
			OccurredAt: event.CreatedAt.UnixMilli(),
		},
	}, nil
}

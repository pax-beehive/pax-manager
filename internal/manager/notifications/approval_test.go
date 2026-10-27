package notifications

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApprovalNotificationGivenPendingRequestThenUsesReviewCopyAndDistinctIdentity(
	t *testing.T,
) {
	now := time.Now().UTC()
	event := ApprovalRequest{
		UserID:     "user",
		NodeID:     "node",
		AgentID:    "agent",
		SessionID:  "session",
		ApprovalID: "approval",
		CreatedAt:  now,
	}
	notification, err := BuildApprovalNotification(event)
	require.NoError(t, err)
	assert.Equal(t, "approval.required", notification.Payload.Type)
	assert.Equal(t, "Approval needed", notification.Payload.Title)
	assert.Equal(
		t,
		"Your session is waiting for your approval. Tap to review the request.",
		notification.Payload.Body,
	)
	assert.Equal(t, "/?session_id=session", notification.Payload.URL)
	assert.Equal(t, event.ApprovalID, notification.Payload.ApprovalID)
	assert.Equal(t, now.Add(time.Hour), notification.ExpiresAt)
	assert.Equal(t, now.UnixMilli(), notification.Payload.OccurredAt)
	replayed, err := BuildApprovalNotification(event)
	require.NoError(t, err)
	assert.Equal(t, notification, replayed)
	event.ApprovalID = "second"
	other, err := BuildApprovalNotification(event)
	require.NoError(t, err)
	assert.NotEqual(t, notification.ID, other.ID)
	turn, err := BuildTurnNotification(
		TurnCompletion{
			UserID:      "user",
			NodeID:      "node",
			AgentID:     "agent",
			SessionID:   "session",
			TurnID:      "approval",
			CompletedAt: now,
		},
	)
	require.NoError(t, err)
	assert.NotEqual(t, notification.ID, turn.ID)
	expiry := now.Add(time.Minute)
	event.ExpiresAt = &expiry
	other, err = BuildApprovalNotification(event)
	require.NoError(t, err)
	assert.Equal(t, expiry, other.ExpiresAt)
}

func TestApprovalNotificationGivenInvalidIdentityOrTimeThenRejectsIt(t *testing.T) {
	valid := ApprovalRequest{
		UserID:     "user",
		NodeID:     "node",
		AgentID:    "agent",
		SessionID:  "session",
		ApprovalID: "approval",
		CreatedAt:  time.Now(),
	}
	for _, revise := range []func(*ApprovalRequest){
		func(e *ApprovalRequest) { e.UserID = "" },
		func(e *ApprovalRequest) { e.NodeID = "" },
		func(e *ApprovalRequest) { e.AgentID = "" },
		func(e *ApprovalRequest) { e.SessionID = "" },
		func(e *ApprovalRequest) { e.ApprovalID = " " },
		func(e *ApprovalRequest) { e.CreatedAt = time.Time{} },
	} {
		event := valid
		revise(&event)
		_, err := BuildApprovalNotification(event)
		assert.ErrorIs(t, err, ErrInvalidApproval)
	}
}

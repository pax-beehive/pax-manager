package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretarySettledSourceGivenQuietBoundaryThenUsesActualSourceWrites(t *testing.T) {
	now := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	message := domain.Message{Status: "received", CreatedAt: old, UpdatedAt: old}
	session := domain.AgentSession{RuntimeStatus: "idle"}
	assert.True(t, secretarySourceSettled(message, session, now.Add(-30*time.Second), now))
	assert.False(
		t,
		secretarySourceSettled(message, session, now.Add(-30*time.Second+time.Nanosecond), now),
	)
	assert.False(t, secretarySourceSettled(message, session, now.Add(time.Second), now))
	message.CreatedAt = now
	assert.False(t, secretarySourceSettled(message, session, old, now))
	message.CreatedAt, message.UpdatedAt = old, now
	assert.False(t, secretarySourceSettled(message, session, old, now))
	message.CreatedAt, message.UpdatedAt = time.Time{}, time.Time{}
	assert.False(t, secretarySourceSettled(message, session, old, now))
}

func TestSecretarySettledSourceGivenContradictoryRuntimeOrStreamingStatusThenNeverAssumesStable(
	t *testing.T,
) {
	now := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	message := domain.Message{Status: "received", CreatedAt: old, UpdatedAt: old}
	session := domain.AgentSession{RuntimeStatus: "idle"}
	for _, lifecycle := range []string{"running", "waiting_approval", "blocked", "cancelling", "unknown"} {
		t.Run(lifecycle, func(t *testing.T) {
			session.RuntimeState = &domain.SessionRuntimeState{Lifecycle: lifecycle}
			assert.False(t, secretarySourceSettled(message, session, old, now))
		})
	}
	session.RuntimeState = &domain.SessionRuntimeState{Lifecycle: "errored"}
	assert.True(t, secretarySourceSettled(message, session, old, now))
	session.RuntimeState = &domain.SessionRuntimeState{Lifecycle: "idle"}
	for _, status := range []string{"streaming", "running", "pending", "in_progress"} {
		message.Status = status
		assert.False(t, secretarySourceSettled(message, session, old, now))
	}
}

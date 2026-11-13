package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func stalledSecretaryRecord(state string, now time.Time) secretaryTaskRecord {
	return secretaryTaskRecord{
		task:           domain.SecretaryTask{TaskID: "1", BatchID: "batch", AttemptID: "attempt"},
		desired:        2,
		processed:      1,
		batchState:     state,
		attemptState:   "unknown",
		enabled:        true,
		blocked:        true,
		attemptCreated: now.Add(-domain.SecretaryAutoRestartDelay(0)),
		attemptUpdated: now.Add(-domain.SecretaryAutoRestartDelay(0)),
	}
}

func TestSecretaryAutoRestartDueGivenStalledTaskThenWaitsForItsOwnCooldown(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	t.Run("unknown outcome is measured from the start of its attempt", func(t *testing.T) {
		record := stalledSecretaryRecord("unknown", now)
		// Rechecking the same attempt refreshes its update time but not its age.
		record.attemptUpdated = now
		state, due := secretaryAutoRestartDue(record, now)
		assert.True(t, due)
		assert.Equal(t, "unknown", state)
		_, due = secretaryAutoRestartDue(record, now.Add(-time.Second))
		assert.False(t, due)
	})
	t.Run("blocked batch is measured from its last failure", func(t *testing.T) {
		record := stalledSecretaryRecord("blocked", now)
		record.attemptState = "invalid"
		record.attemptCreated = now.Add(-24 * time.Hour)
		state, due := secretaryAutoRestartDue(record, now)
		assert.True(t, due)
		assert.Equal(t, "blocked", state)
		_, due = secretaryAutoRestartDue(record, now.Add(-time.Second))
		assert.False(t, due)
	})
	t.Run("pending batch behind a blocked job is stalled too", func(t *testing.T) {
		// A manual recheck that ends in a retryable failure leaves this state.
		record := stalledSecretaryRecord("pending", now)
		record.attemptState = "invalid"
		state, due := secretaryAutoRestartDue(record, now)
		assert.True(t, due)
		assert.Equal(t, "pending", state)
	})
	t.Run("each restart waits longer than the previous one", func(t *testing.T) {
		record := stalledSecretaryRecord("unknown", now)
		record.autoRestarts = 1
		_, due := secretaryAutoRestartDue(record, now)
		assert.False(t, due)
		record.attemptCreated = now.Add(-domain.SecretaryAutoRestartDelay(1))
		_, due = secretaryAutoRestartDue(record, now)
		assert.True(t, due)
	})
}

func TestSecretaryAutoRestartDueGivenWorkThatMustNotBeRestartedThenLeavesItAlone(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for name, change := range map[string]func(*secretaryTaskRecord){
		"secretary disabled":       func(r *secretaryTaskRecord) { r.enabled = false },
		"task can still progress":  func(r *secretaryTaskRecord) { r.blocked = false },
		"owner paused it":          func(r *secretaryTaskRecord) { r.task.Paused = true },
		"lease is active":          func(r *secretaryTaskRecord) { r.task.LeaseActive = true },
		"nothing is pending":       func(r *secretaryTaskRecord) { r.processed = r.desired },
		"budget is spent":          func(r *secretaryTaskRecord) { r.autoRestarts = domain.SecretaryAutoRestartMax },
		"batch is still running":   func(r *secretaryTaskRecord) { r.batchState = "running" },
		"output awaits publishing": func(r *secretaryTaskRecord) { r.batchState = "output_received" },
		"attempt time is missing":  func(r *secretaryTaskRecord) { r.attemptCreated = time.Time{} },
		"prompt never fit: no attempt exists": func(r *secretaryTaskRecord) {
			r.batchState, r.task.AttemptID, r.attemptState = "blocked", "", ""
		},
		"input is deterministically too large": func(r *secretaryTaskRecord) {
			r.batchState, r.attemptState = "blocked", "failed"
			r.failure = secretaryInputLimitPrefix + " source exceeds the limit"
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := stalledSecretaryRecord("unknown", now)
			change(&record)
			_, due := secretaryAutoRestartDue(record, now.Add(48*time.Hour))
			assert.False(t, due)
		})
	}
}

func TestMemorySecretaryJobGivenRestartHistoryThenCountsOnlyTheUnbrokenChain(t *testing.T) {
	restarted := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	job := &memorySecretaryJob{autoRestartCount: 2, autoRestartEpoch: 4, autoRestartedAt: restarted}
	job.work.RetryEpoch = 4
	assert.Equal(t, 2, job.consecutiveAutoRestarts())

	earlier := restarted.Add(-time.Minute)
	job.completed = &earlier
	assert.Equal(
		t,
		2,
		job.consecutiveAutoRestarts(),
		"an older publication does not restore the budget",
	)

	later := restarted.Add(time.Minute)
	job.completed = &later
	assert.Zero(
		t,
		job.consecutiveAutoRestarts(),
		"publishing after the restart starts a new budget",
	)

	job.completed = nil
	job.work.RetryEpoch = 5
	assert.Zero(t, job.consecutiveAutoRestarts(), "an explicit retry moves to another epoch")

	assert.Zero(t, (&memorySecretaryJob{autoRestartCount: 3}).consecutiveAutoRestarts())
}

func TestSecretaryAutoRestartLimitGivenUnboundedRequestThenUsesTheDefaultPage(t *testing.T) {
	assert.Equal(t, 100, secretaryAutoRestartLimit(0))
	assert.Equal(t, 100, secretaryAutoRestartLimit(-1))
	assert.Equal(t, 100, secretaryAutoRestartLimit(101))
	assert.Equal(t, 7, secretaryAutoRestartLimit(7))
}

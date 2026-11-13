package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// stalledSecretaryRecord is a task behind a blocked job whose attempt started
// and last changed exactly one first cooldown ago.
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
		// One model call spent so far.
		dispatchedAttempts: 1,
	}
}

func TestSecretaryAutoRecoveryDueGivenStalledTaskThenWaitsForItsOwnCooldown(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	t.Run("unknown outcome is measured from the start of its attempt", func(t *testing.T) {
		record := stalledSecretaryRecord("unknown", now)
		// Rechecking the same attempt refreshes its update time but not its age.
		record.attemptUpdated = now
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		assert.Equal(t, secretaryAutoRecoveryNone,
			secretaryAutoRecoveryDue(record, now.Add(-time.Second)))
	})
	t.Run("interrupted attempt behind a blocked job is measured the same way", func(t *testing.T) {
		record := stalledSecretaryRecord("running", now)
		record.attemptState, record.attemptUpdated = "running", now
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		record.blocked = false
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now),
			"an unblocked interrupted attempt is still reclaimed by the worker")
	})
	t.Run("blocked batch is measured from its last failure", func(t *testing.T) {
		record := stalledSecretaryRecord("blocked", now)
		record.attemptState = "invalid"
		record.attemptCreated = now.Add(-24 * time.Hour)
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		assert.Equal(t, secretaryAutoRecoveryNone,
			secretaryAutoRecoveryDue(record, now.Add(-time.Second)))
	})
	t.Run("pending batch behind a blocked job is stalled too", func(t *testing.T) {
		// A manual recheck that ends in a retryable failure leaves this state.
		record := stalledSecretaryRecord("pending", now)
		record.attemptState = "invalid"
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
	})
	t.Run("each restart waits longer than the previous one", func(t *testing.T) {
		record := stalledSecretaryRecord("unknown", now)
		record.autoRestarts = 1
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
		record.attemptCreated = now.Add(-domain.SecretaryAutoRestartDelay(1))
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
	})
}

func TestSecretaryAutoRecoveryDueGivenUnclaimableJobThenDoesNotDependOnTheBlockedFlag(
	t *testing.T,
) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	t.Run("exhausted connection retries after a new source message", func(t *testing.T) {
		// A new message clears the flag, but claims still skip the exhausted batch.
		record := stalledSecretaryRecord("blocked", now)
		record.blocked, record.attemptState, record.notDispatched = false, "failed", true
		record.task.ConnectionFailures = domain.SecretaryConnectionMaxRetries + 1
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
	})
	t.Run("guided operation that needs attention", func(t *testing.T) {
		record := stalledSecretaryRecord("unknown", now)
		record.blocked, record.operationStalled, record.operationPending = false, true, true
		record.task.OperationID = "operation"
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
	})
	t.Run("rejected output without either reason is left to new input", func(t *testing.T) {
		// Without the flag the next claim builds a fresh batch from new messages.
		record := stalledSecretaryRecord("blocked", now)
		record.blocked, record.attemptState = false, "invalid"
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
	})
}

func TestSecretaryAutoRecoveryDueGivenSpentModelCallsThenBudgetsRestartsByCost(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	aged := func(record secretaryTaskRecord, dispatched int) secretaryTaskRecord {
		record.attemptCreated = now.Add(-30 * 24 * time.Hour)
		record.attemptUpdated = record.attemptCreated
		record.dispatchedAttempts = dispatched
		return record
	}
	t.Run("a batch that used up its attempts gets one fresh start", func(t *testing.T) {
		record := aged(stalledSecretaryRecord("blocked", now), 3)
		record.attemptState = "invalid"
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		record.autoRestarts = 1
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
	})
	t.Run("a retried batch that ends unknown is budgeted by its calls too", func(t *testing.T) {
		// Two rejected outputs and a third attempt without a terminal proof.
		record := aged(stalledSecretaryRecord("unknown", now), 3)
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		record.autoRestarts = 1
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
		record.dispatchedAttempts = 2
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
	})
	t.Run("a single unknown outcome gets the full budget", func(t *testing.T) {
		record := aged(stalledSecretaryRecord("unknown", now), 1)
		record.autoRestarts = domain.SecretaryAutoRestartMax - 1
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		record.autoRestarts = domain.SecretaryAutoRestartMax
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
	})
	t.Run("exhausted connection retries cost no model call", func(t *testing.T) {
		record := aged(stalledSecretaryRecord("blocked", now), 0)
		record.attemptState, record.notDispatched = "failed", true
		record.task.ConnectionFailures = domain.SecretaryConnectionMaxRetries + 1
		record.autoRestarts = domain.SecretaryAutoRestartMax - 1
		assert.Equal(t, secretaryAutoRecoveryRestart, secretaryAutoRecoveryDue(record, now))
		record.autoRestarts = domain.SecretaryAutoRestartMax
		assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now))
	})
}

func TestSecretaryAutoRecoveryDueGivenSavedOutputThenReleasesItOnceWithoutARestart(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	record := stalledSecretaryRecord("output_received", now)
	record.attemptState = "output_received"
	// A spent restart budget must not keep saved output from being published.
	record.autoRestarts, record.dispatchedAttempts = domain.SecretaryAutoRestartMax, 3
	assert.Equal(t, secretaryAutoRecoveryResume, secretaryAutoRecoveryDue(record, now))
	assert.Equal(t, secretaryAutoRecoveryNone,
		secretaryAutoRecoveryDue(record, now.Add(-time.Second)))

	released := record
	released.autoResumedAttempt = record.task.AttemptID
	assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(released, now),
		"a job blocked again behind the same output must not be released in a loop")
	released.autoResumedAttempt = "an-earlier-attempt"
	assert.Equal(t, secretaryAutoRecoveryResume, secretaryAutoRecoveryDue(released, now))

	record.blocked = false
	assert.Equal(t, secretaryAutoRecoveryNone, secretaryAutoRecoveryDue(record, now),
		"unblocked saved output is already published by the worker")
}

func TestSecretaryAutoRecoveryDueGivenWorkThatMustNotBeRecoveredThenLeavesItAlone(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for name, change := range map[string]func(*secretaryTaskRecord){
		"secretary disabled":      func(r *secretaryTaskRecord) { r.enabled = false },
		"task can still progress": func(r *secretaryTaskRecord) { r.blocked = false },
		"owner paused it":         func(r *secretaryTaskRecord) { r.task.Paused = true },
		"lease is active":         func(r *secretaryTaskRecord) { r.task.LeaseActive = true },
		"nothing is pending":      func(r *secretaryTaskRecord) { r.processed = r.desired },
		"batch was committed":     func(r *secretaryTaskRecord) { r.batchState = "committed" },
		"attempt time is missing": func(r *secretaryTaskRecord) { r.attemptCreated = time.Time{} },
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
			assert.Equal(t, secretaryAutoRecoveryNone,
				secretaryAutoRecoveryDue(record, now.Add(48*time.Hour)))
		})
	}
}

func TestMemorySecretaryJobGivenRestartHistoryThenCountsOnlyTheUnbrokenChain(t *testing.T) {
	restarted := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	settings := restarted.Add(-time.Hour)
	job := &memorySecretaryJob{autoRestartCount: 2, autoRestartEpoch: 4, autoRestartedAt: restarted}
	job.work.RetryEpoch = 4
	assert.Equal(t, 2, job.consecutiveAutoRestarts(settings))

	earlier := restarted.Add(-time.Minute)
	job.completed = &earlier
	assert.Equal(t, 2, job.consecutiveAutoRestarts(settings),
		"an older publication does not restore the budget")

	later := restarted.Add(time.Minute)
	job.completed = &later
	assert.Zero(t, job.consecutiveAutoRestarts(settings),
		"publishing after the restart starts a new budget")

	job.completed = nil
	assert.Zero(t, job.consecutiveAutoRestarts(restarted.Add(time.Minute)),
		"changing the settings after the restart starts a new budget")

	job.work.RetryEpoch = 5
	assert.Zero(t, job.consecutiveAutoRestarts(settings),
		"an explicit retry moves to another epoch")

	assert.Zero(t, (&memorySecretaryJob{autoRestartCount: 3}).consecutiveAutoRestarts(settings))
}

func TestSecretaryAutoRestartLimitGivenUnboundedRequestThenUsesTheDefaultPage(t *testing.T) {
	assert.Equal(t, 100, secretaryAutoRestartLimit(0))
	assert.Equal(t, 100, secretaryAutoRestartLimit(-1))
	assert.Equal(t, 100, secretaryAutoRestartLimit(101))
	assert.Equal(t, 7, secretaryAutoRestartLimit(7))
}

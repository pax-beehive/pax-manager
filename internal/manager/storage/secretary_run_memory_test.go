package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryRunMemoryGivenRepeatedRequestThenReturnsImmutableReceiptWithoutChangingJobs(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	before := *store.secretaryState.jobs["1"]
	first, err := store.RequestSecretaryRun(t.Context(), "owner", "request-1")
	require.NoError(t, err)
	assert.Equal(t, "request-1", first.RequestID)
	assert.Equal(t, "accepted", first.State)
	assert.Equal(t, before, *store.secretaryState.jobs["1"])
	cfg := store.secretaryState.settings["owner"]
	cfg.Enabled = false
	store.secretaryState.settings["owner"] = cfg
	replayed, err := store.RequestSecretaryRun(t.Context(), "owner", "request-1")
	require.NoError(t, err)
	assert.Equal(t, first, replayed)
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "different")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestSecretaryRunMemoryGivenConcurrentDuplicatesThenAcceptsOneCommand(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	var group sync.WaitGroup
	results := make(chan domain.SecretaryRunReceipt, 8)
	errors := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			receipt, err := store.RequestSecretaryRun(t.Context(), "owner", "same-request")
			results <- receipt
			errors <- err
		})
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var first domain.SecretaryRunReceipt
	for receipt := range results {
		if first.RequestID == "" {
			first = receipt
		}
		assert.Equal(t, first, receipt)
	}
	assert.Len(t, store.secretaryState.runCommands, 1)
}

func TestSecretaryRunMemoryGivenBoundedCommandThenRetriesMetadataOnlyOnce(t *testing.T) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	base := *store.secretaryState.jobs["1"]
	for i := 1; i <= 13; i++ {
		id := fmt.Sprint(i)
		sessionID := "session-" + id
		store.sessions[sessionKey("source", sessionID)] = AgentSession{
			ID:        int64(i),
			AgentID:   "source",
			SessionID: sessionID,
		}
		job := base
		job.work.SourceID, job.work.SourceSessionID = id, sessionID
		job.work.RetryGeneration, job.work.RetryEpoch = 3, 4
		job.available, job.lastError, job.executionBlocked = now.Add(
			time.Hour,
		), "known failure", true
		job.autoRetryKey, job.autoRetryCount = "old", 2
		job.work.ValidationFeedback = "old feedback"
		store.secretaryState.jobs[id] = &job
	}
	_, err := store.RequestSecretaryRun(t.Context(), "owner", "bounded")
	require.NoError(t, err)
	for step := 1; step <= 5; step++ {
		worked, advanceErr := store.AdvanceSecretaryRuns(t.Context(), 3)
		require.NoError(t, advanceErr)
		assert.True(t, worked)
		changed := 0
		for _, job := range store.secretaryState.jobs {
			if job.work.RetryGeneration == 4 {
				changed++
				assert.EqualValues(t, 5, job.work.RetryEpoch)
				assert.Empty(t, job.lastError)
				assert.False(t, job.executionBlocked)
				assert.Empty(t, job.autoRetryKey)
				assert.Zero(t, job.autoRetryCount)
				assert.Empty(t, job.work.ValidationFeedback)
				assert.Equal(t, *now, job.available)
				assert.Equal(t, base.desired, job.desired)
				assert.Equal(t, base.processed, job.processed)
				assert.Equal(t, base.checkpoint, job.checkpoint)
			}
		}
		assert.Equal(t, min(step*3, 13), changed)
	}
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "bounded")
	require.NoError(t, err)
	worked, err := store.AdvanceSecretaryRuns(t.Context(), 128)
	require.NoError(t, err)
	assert.False(t, worked)
}

func TestSecretaryRunMemoryGivenUnsafeOrHiddenJobThenDoesNotChangeIt(t *testing.T) {
	for _, reason := range []string{"paused", "leased", "completed", "foreign", "archived", "internal", "prefix", "registered", "missing-node", "pending", "running", "output_received", "unknown", "old-unknown"} {
		t.Run(reason, func(t *testing.T) {
			store, now := secretaryMemoryFixture(t)
			secretaryEnable(t, store)
			job := store.secretaryState.jobs["1"]
			session := store.sessions[sessionKey("source", "session")]
			switch reason {
			case "paused":
				job.paused = true
			case "leased":
				job.work.LeaseToken, job.leaseUntil = "active", now.Add(time.Hour)
			case "completed":
				job.processed = job.desired
			case "foreign":
				agent := store.agents["source"]
				agent.OwnerUserID = "other"
				store.agents["source"] = agent
			case "archived":
				session.ArchivedAt = now
			case "internal":
				session.Source = domain.SecretaryInternalSource
			case "prefix":
				delete(store.sessions, sessionKey("source", "session"))
				session.SessionID, job.work.SourceSessionID = "sess_secretary_private", "sess_secretary_private"
			case "registered":
				store.secretaryState.internal[sessionKey("source", "session")] = true
			case "missing-node":
				session.NodeID = "missing"
			default:
				state, epoch := reason, job.work.RetryEpoch
				if reason == "old-unknown" {
					state, epoch = "unknown", epoch-1
				}
				store.secretaryExecutionsLocked()["unresolved"] = &memorySecretaryExecutionBatch{
					source: "1", owner: "owner", state: state, epoch: epoch, settings: 0,
				}
			}
			store.sessions[sessionKey(session.AgentID, session.SessionID)] = session
			before := *job
			_, err := store.RequestSecretaryRun(t.Context(), "owner", "protected")
			require.NoError(t, err)
			_, err = store.AdvanceSecretaryRuns(t.Context(), 128)
			require.NoError(t, err)
			assert.Equal(t, before, *job)
		})
	}
}

func TestSecretaryRunMemoryGivenChangedSettingsOrPolicyThenCancelsCommandWithoutJobChanges(
	t *testing.T,
) {
	for _, change := range []string{"disabled", "revision", "policy"} {
		t.Run(change, func(t *testing.T) {
			store, _ := secretaryMemoryFixture(t)
			secretaryEnable(t, store)
			_, err := store.RequestSecretaryRun(t.Context(), "owner", "stale")
			require.NoError(t, err)
			cfg := store.secretaryState.settings["owner"]
			switch change {
			case "disabled":
				cfg.Enabled = false
			case "revision":
				cfg.Revision++
			case "policy":
				for _, command := range store.secretaryState.runCommands {
					command.version = "obsolete"
				}
			}
			store.secretaryState.settings["owner"] = cfg
			before := *store.secretaryState.jobs["1"]
			worked, err := store.AdvanceSecretaryRuns(t.Context(), 0)
			require.NoError(t, err)
			assert.True(t, worked)
			assert.Equal(t, before, *store.secretaryState.jobs["1"])
			for _, command := range store.secretaryState.runCommands {
				assert.Equal(t, "canceled", command.state)
			}
		})
	}
}

func TestSecretaryRunMemoryGivenInvalidRequestOrCanceledContextThenDoesNotQueue(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	_, err := store.RequestSecretaryRun(t.Context(), "", "id")
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "invalid id")
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "disabled")
	assert.ErrorIs(t, err, ErrConflict)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.RequestSecretaryRun(ctx, "owner", "canceled")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = store.AdvanceSecretaryRuns(ctx, 1)
	assert.ErrorIs(t, err, context.Canceled)
}

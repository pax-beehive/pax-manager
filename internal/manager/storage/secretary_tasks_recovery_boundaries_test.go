package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryTasksGivenPausedDispatchedInputChangedThenResumeDoesNotAuthorizeANewEpoch(
	t *testing.T,
) {
	for _, state := range []string{"unknown", "running"} {
		t.Run(state, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				var original domain.SecretaryJob
				if state == "unknown" {
					original = secretaryTaskUnknown(t, f, "provider failure")
				} else {
					original = f.begin(t, f.prepare(t))
					f.expire(t)
				}
				store := secretaryTasksStore(t, f)
				observed := secretaryReadTask(t, f).Task
				_, err := store.ControlSecretaryTask(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(observed, "pause"),
				)
				require.NoError(t, err)
				f.editConnectionSource(t, 23)
				require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
				observed = secretaryReadTask(t, f).Task
				_, err = store.ControlSecretaryTask(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(observed, "resume"),
				)
				require.NoError(t, err)
				f.releaseConnectionBackoff(t)
				jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
				require.NoError(t, err)
				if len(jobs) > 0 {
					_, err = f.store.PrepareSecretaryBatch(t.Context(), jobs[0])
					assert.ErrorIs(t, err, ErrConflict)
				}
				current := secretaryReadTask(t, f).Task
				assert.Equal(t, original.RetryEpoch, current.RetryEpoch)
				assert.Equal(t, original.BatchID, current.BatchID)
				assert.Equal(t, original.AttemptID, current.AttemptID)
				assert.Equal(t, 1, f.attemptCount(t, original.BatchID))
				jobs, err = f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
				require.NoError(t, err)
				assert.Empty(t, jobs)
				retry := secretaryTaskObserved(current, "retry")
				retry.ConfirmRestart = true
				restarted, err := store.ControlSecretaryTask(t.Context(), "owner", "1", retry)
				require.NoError(t, err)
				assert.Greater(t, restarted.RetryEpoch, original.RetryEpoch)
			})
		})
	}
}

func TestSecretaryTasksGivenSuccessfulRecoveryWithPendingDeltaThenSchedulesItWithoutAnotherWake(
	t *testing.T,
) {
	for _, timing := range []string{"before_claim", "after_claim"} {
		t.Run(timing, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				store := secretaryTasksStore(t, f)
				original := f.begin(t, f.prepare(t))
				require.NoError(
					t,
					f.store.SaveSecretaryAttemptOutput(
						t.Context(),
						original,
						original.AttemptID,
						"saved output",
						domain.SecretaryToolReads{},
					),
				)
				f.expire(t)
				if timing == "before_claim" {
					f.addMessage(t)
				}
				observed := secretaryReadTask(t, f).Task
				job, err := store.ClaimSecretaryTaskRecovery(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(observed, "recheck"),
					time.Minute,
				)
				require.NoError(t, err)
				if timing == "after_claim" {
					f.addMessage(t)
				}
				_, err = f.store.PublishSecretaryBatch(
					t.Context(),
					job,
					secretaryNodesPublication(job, "recovered"),
				)
				require.NoError(t, err)
				require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), job))
				next := secretaryClaim(t, f.store)
				require.Len(t, next.Messages, 1)
				assert.Equal(t, "m2", next.Messages[0].SourceID)
			})
		})
	}
}

func TestSecretaryTasksGivenPausedUnknownThenBulkRetryLeavesItsEpochAndExecutionBlocked(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryTaskUnknown(t, f, "provider outcome unknown")
		store := secretaryTasksStore(t, f)
		observed := secretaryReadTask(t, f).Task
		_, err := store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(observed, "pause"),
		)
		require.NoError(t, err)
		before := secretaryReadTask(t, f)
		state := f.connectionState(t, job)
		require.True(t, state.blocked)
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		after := secretaryReadTask(t, f)
		assert.Equal(t, before, after, "bulk retry must exclude paused task metadata and history")
		assert.Equal(t, state, f.connectionState(t, job))
		_, err = store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(after.Task, "resume"),
		)
		require.NoError(t, err)
		f.releaseConnectionBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}

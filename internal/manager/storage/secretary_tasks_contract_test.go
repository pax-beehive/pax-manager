package storage

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryTasksStore(t *testing.T, f *secretaryLifecycleFixture) domain.SecretaryTaskStore {
	t.Helper()
	store, ok := f.store.(domain.SecretaryTaskStore)
	require.True(t, ok)
	return store
}

func secretaryTaskObserved(task domain.SecretaryTask, action string) domain.SecretaryTaskAction {
	return domain.SecretaryTaskAction{Action: action, ExpectedRevision: task.Revision,
		BatchID: task.BatchID, AttemptID: task.AttemptID, RetryEpoch: task.RetryEpoch,
		SettingsRevision: task.SettingsRevision}
}

func secretaryReadTask(t *testing.T, f *secretaryLifecycleFixture) domain.SecretaryTaskDetail {
	t.Helper()
	detail, err := secretaryTasksStore(
		t,
		f,
	).GetSecretaryTask(t.Context(), "owner", "1", domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	return detail
}

func secretaryTaskUnknown(
	t *testing.T,
	f *secretaryLifecycleFixture,
	cause string,
) domain.SecretaryJob {
	t.Helper()
	job := f.begin(t, f.prepare(t))
	failure := errors.Join(domain.ErrSecretaryExecutionUnknown, errors.New(cause))
	require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure))
	require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, failure))
	return job
}

func TestSecretaryTasksGivenNativeConnectionFailureThenReturnsSafeMetadataAndRequiresConfirmedRetry(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryTaskUnknown(
			t,
			f,
			"Internal error: The model connection failed. This request did not complete. You can retry.",
		)
		store := secretaryTasksStore(t, f)
		detail := secretaryReadTask(t, f)
		assert.Equal(t, "unknown", detail.Task.ExecutionState)
		assert.Equal(t, "model_connection_failed", detail.Task.ErrorCode)
		assert.Empty(t, detail.Task.ExecutionSessionID)
		require.Len(t, detail.Attempts, 1)
		assert.Equal(t, "model_connection_failed", detail.Attempts[0].ErrorCode)
		assert.False(t, detail.Attempts[0].HasOutput)
		action := secretaryTaskObserved(detail.Task, "retry")
		_, err := store.ControlSecretaryTask(t.Context(), "owner", "1", action)
		assert.ErrorIs(t, err, ErrConflict)
		action.ConfirmRestart = true
		retried, err := store.ControlSecretaryTask(t.Context(), "owner", "1", action)
		require.NoError(t, err)
		assert.Equal(t, detail.Task.Revision+1, retried.Revision)
		assert.Equal(t, job.RetryEpoch+1, retried.RetryEpoch)
		assert.Empty(t, retried.BatchID)
		assert.Empty(t, retried.AttemptID)
		assert.False(t, retried.Paused)
		assert.Equal(t, "queued", retried.ExecutionState)
		assert.Equal(
			t,
			1,
			f.attemptCount(t, job.BatchID),
			"the control endpoint must never send a model request",
		)
		_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", action)
		assert.ErrorIs(
			t,
			err,
			ErrConflict,
			"lost responses must be refetched instead of blindly retried",
		)
	})
}

func TestSecretaryTasksGivenPauseDuringExecutionThenLetsItFinishAndKeepsFutureClaimsPaused(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := secretaryTasksStore(t, f)
		job := f.begin(t, f.prepare(t))
		detail := secretaryReadTask(t, f)
		require.True(t, detail.Task.LeaseActive)
		paused, err := store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(detail.Task, "pause"),
		)
		require.NoError(t, err)
		assert.True(t, paused.Paused)
		active, err := f.store.SecretaryJobActive(t.Context(), job)
		require.NoError(t, err)
		assert.True(t, active)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved output",
				domain.SecretaryToolReads{},
			),
		)
		_, err = f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "first"),
		)
		require.NoError(t, err)
		f.addMessage(t)
		f.editConnectionSource(t, 1)
		f.restart()
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		detail = secretaryReadTask(t, f)
		assert.True(t, detail.Task.Paused)
		_, err = secretaryTasksStore(
			t,
			f,
		).ControlSecretaryTask(t.Context(), "owner", "1", secretaryTaskObserved(detail.Task, "resume"))
		require.NoError(t, err)
		jobs, err = f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Len(t, jobs, 1)
	})
}

func TestSecretaryTasksGivenExplicitRecheckThenLeasesTheSameAttemptAndUnresolvedRemainsBlocked(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		original := secretaryTaskUnknown(
			t,
			f,
			"Internal provider failure with private prompt material",
		)
		store := secretaryTasksStore(t, f)
		detail := secretaryReadTask(t, f)
		raw, err := json.Marshal(detail)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "private prompt material")
		action := secretaryTaskObserved(detail.Task, "recheck")
		job, err := store.ClaimSecretaryTaskRecovery(t.Context(), "owner", "1", action, time.Minute)
		require.NoError(t, err)
		assert.Equal(t, original.BatchID, job.BatchID)
		assert.Equal(t, original.AttemptID, job.AttemptID)
		assert.Equal(t, original.RetryEpoch, job.RetryEpoch)
		require.NotNil(t, job.Attempt)
		assert.Equal(t, "unknown", job.Attempt.State)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
		_, err = store.ClaimSecretaryTaskRecovery(t.Context(), "owner", "1", action, time.Minute)
		assert.ErrorIs(t, err, ErrConflict)
		current := secretaryReadTask(t, f).Task
		assert.Equal(t, detail.Task.Revision+1, current.Revision)
		_, err = store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(current, "retry"),
		)
		assert.ErrorIs(t, err, ErrConflict)
		_, err = store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(current, "pause"),
		)
		require.NoError(t, err)
		require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), job))
		current = secretaryReadTask(t, f).Task
		assert.True(t, current.Paused)
		assert.False(t, current.LeaseActive)
		assert.Equal(t, "unknown", current.ExecutionState)
		_, err = store.ControlSecretaryTask(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(current, "resume"),
		)
		require.NoError(t, err)
		f.releaseConnectionBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs, "checking a result must never schedule another model execution")
		stale := job
		stale.LeaseToken = "stale-token"
		assert.ErrorIs(
			t,
			store.ReleaseSecretaryTaskRecovery(t.Context(), stale),
			domain.ErrSecretaryFenced,
		)
	})
}

func TestSecretaryTasksGivenSavedOutputThenRecoversItWithoutAnotherAttemptAndPreservesNewerLease(
	t *testing.T,
) {
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
		detail := secretaryReadTask(t, f)
		job, err := store.ClaimSecretaryTaskRecovery(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(detail.Task, "recheck"),
			time.Minute,
		)
		require.NoError(t, err)
		require.NotNil(t, job.Attempt)
		assert.Equal(t, "saved output", job.Attempt.Output)
		_, err = f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "recovered"),
		)
		require.NoError(t, err)
		require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), job))
		assert.Equal(t, "completed", secretaryReadTask(t, f).Task.ExecutionState)
		page, err := store.ListSecretaryTasks(t.Context(), "owner", domain.SecretaryTaskQuery{})
		require.NoError(t, err)
		assert.Empty(t, page.Items)
		f.addMessage(t)
		newer := secretaryClaim(t, f.store)
		require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), job))
		active, err := f.store.SecretaryJobActive(t.Context(), newer)
		require.NoError(t, err)
		assert.True(t, active, "a late release must not clear the newer worker's lease")
	})
}

func TestSecretaryTasksGivenChangedSourceOrCASMismatchThenRejectsRecoveryWithoutChangingEpoch(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryTaskUnknown(t, f, "provider outcome unknown")
		store := secretaryTasksStore(t, f)
		detail := secretaryReadTask(t, f)
		action := secretaryTaskObserved(detail.Task, "recheck")
		stale := action
		stale.SettingsRevision++
		_, err := store.ClaimSecretaryTaskRecovery(t.Context(), "owner", "1", stale, time.Minute)
		assert.ErrorIs(t, err, ErrConflict)
		f.editConnectionSource(t, 17)
		_, err = store.ClaimSecretaryTaskRecovery(t.Context(), "owner", "1", action, time.Minute)
		assert.ErrorIs(t, err, ErrConflict)
		current := secretaryReadTask(t, f).Task
		assert.Equal(t, job.RetryEpoch, current.RetryEpoch)
		assert.Equal(t, detail.Task.Revision, current.Revision)
		assert.False(t, current.LeaseActive)
	})
}

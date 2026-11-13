package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// age moves every execution timestamp into the past, as if elapsed had passed
// since the fixture last changed.
func (f *secretaryLifecycleFixture) age(t *testing.T, elapsed time.Duration) {
	t.Helper()
	if f.memory != nil {
		*f.now = f.now.Add(elapsed)
		return
	}
	for _, statement := range []string{
		`UPDATE secretary_attempts SET created_at=created_at-make_interval(secs=>$1),
 updated_at=updated_at-make_interval(secs=>$1)`,
		`UPDATE secretary_batches SET created_at=created_at-make_interval(secs=>$1),
 updated_at=updated_at-make_interval(secs=>$1)`,
		`UPDATE secretary_jobs SET auto_restarted_at=auto_restarted_at-make_interval(secs=>$1),
 completed_at=completed_at-make_interval(secs=>$1),available_at=available_at-make_interval(secs=>$1)`,
	} {
		_, err := f.postgres.db.ExecContext(t.Context(), statement, elapsed.Seconds())
		require.NoError(t, err)
	}
}

// stall leaves the source in a state that previously required an owner action.
func (f *secretaryLifecycleFixture) stall(t *testing.T, failure error) domain.SecretaryJob {
	t.Helper()
	job := f.begin(t, f.prepare(t))
	require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure))
	// The worker releases the claim after recording the attempt failure.
	require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, failure))
	return job
}

func (f *secretaryLifecycleFixture) restartStalled(t *testing.T) []domain.SecretaryTaskRestart {
	t.Helper()
	restarted, err := f.store.(domain.SecretaryAutoRestartStore).
		RestartStalledSecretaryTasks(t.Context(), 0)
	require.NoError(t, err)
	return restarted
}

func (f *secretaryLifecycleFixture) control(t *testing.T, id, action string) {
	t.Helper()
	tasks := f.store.(domain.SecretaryTaskStore)
	detail, err := tasks.GetSecretaryTask(t.Context(), "owner", id, domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	_, err = tasks.ControlSecretaryTask(t.Context(), "owner", id, domain.SecretaryTaskAction{
		Action:           action,
		ExpectedRevision: detail.Task.Revision,
		BatchID:          detail.Task.BatchID,
		AttemptID:        detail.Task.AttemptID,
		RetryEpoch:       detail.Task.RetryEpoch,
		SettingsRevision: detail.Task.SettingsRevision,
		ConfirmRestart:   true,
	})
	require.NoError(t, err)
}

func TestSecretaryAutoRestartGivenUnknownOutcomeThenRestartsOnlyAfterItsCooldown(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		stalled := f.stall(t, domain.ErrSecretaryExecutionUnknown)
		assert.Empty(t, f.restartStalled(t), "a terminal proof may still arrive")
		f.age(t, domain.SecretaryAutoRestartDelay(0)-time.Minute)
		assert.Empty(t, f.restartStalled(t))
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs, "the unknown outcome still blocks ordinary claims")

		f.age(t, 2*time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, domain.SecretaryTaskRestart{
			OwnerUserID: "owner", TaskID: stalled.SourceID, PreviousState: "unknown", Restarts: 1,
		}, restarted[0])
		assert.Empty(t, f.restartStalled(t), "one restart must not be applied twice")

		fresh := f.begin(t, f.prepare(t))
		assert.NotEqual(t, stalled.BatchID, fresh.BatchID)
		assert.Equal(t, stalled.RetryEpoch+1, fresh.RetryEpoch)
		assert.NotEqual(t, stalled.AttemptID, fresh.AttemptID)
		// The replaced attempt stays fenced in its old epoch.
		assert.ErrorIs(t, f.store.SaveSecretaryAttemptOutput(
			t.Context(),
			stalled,
			stalled.AttemptID,
			"late output",
			domain.SecretaryToolReads{},
		), domain.ErrSecretaryFenced)
	})
}

func TestSecretaryAutoRestartGivenExhaustedOutputRetriesThenStartsAFreshBatch(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		var blocked domain.SecretaryJob
		for range domain.SecretaryMaxValidationRetries + 1 {
			blocked = f.stall(t, validationFailure())
			f.releaseBackoff(t)
		}
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		require.Empty(t, jobs, "the exhausted batch must be blocked")
		assert.Empty(t, f.restartStalled(t))

		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, "blocked", restarted[0].PreviousState)
		fresh := f.begin(t, f.prepare(t))
		assert.NotEqual(t, blocked.BatchID, fresh.BatchID)
		assert.Equal(
			t,
			1,
			f.attemptCount(t, fresh.BatchID),
			"a restart receives a new attempt budget",
		)
	})
}

func TestSecretaryAutoRestartGivenRepeatedStallsThenStopsAtItsBudgetUntilTheOwnerRetries(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		var stalled domain.SecretaryJob
		for restart := 1; restart <= domain.SecretaryAutoRestartMax; restart++ {
			stalled = f.stall(t, domain.ErrSecretaryExecutionUnknown)
			f.age(t, domain.SecretaryAutoRestartDelay(restart-1)-time.Minute)
			require.Empty(
				t,
				f.restartStalled(t),
				"restart %d must wait for its longer cooldown",
				restart,
			)
			f.age(t, 2*time.Minute)
			restarted := f.restartStalled(t)
			require.Len(t, restarted, 1)
			assert.Equal(t, restart, restarted[0].Restarts)
		}
		stalled = f.stall(t, domain.ErrSecretaryExecutionUnknown)
		f.age(t, 7*24*time.Hour)
		assert.Empty(
			t,
			f.restartStalled(t),
			"a spent budget must not keep spending the owner's agent",
		)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)

		f.control(t, stalled.SourceID, "retry")
		f.stall(t, domain.ErrSecretaryExecutionUnknown)
		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, 1, restarted[0].Restarts, "an explicit owner retry starts a new budget")
	})
}

func TestSecretaryAutoRestartGivenPublicationAfterRestartThenRestoresTheBudget(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		f.stall(t, domain.ErrSecretaryExecutionUnknown)
		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		require.Len(t, f.restartStalled(t), 1)

		f.age(t, time.Second)
		job := f.begin(t, f.prepare(t))
		require.NoError(t, f.store.SaveSecretaryAttemptOutput(t.Context(), job, job.AttemptID,
			"saved", domain.SecretaryToolReads{}))
		_, err := f.store.PublishSecretaryBatch(t.Context(), job,
			secretaryNodesPublication(job, "decision"))
		require.NoError(t, err)

		f.addMessage(t)
		f.stall(t, domain.ErrSecretaryExecutionUnknown)
		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, 1, restarted[0].Restarts, "real progress must restore the full budget")
	})
}

func TestSecretaryAutoRestartGivenPausedDisabledOrProgressingWorkThenLeavesItAlone(t *testing.T) {
	t.Run("paused by the owner", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			stalled := f.stall(t, domain.ErrSecretaryExecutionUnknown)
			f.control(t, stalled.SourceID, "pause")
			f.age(t, 24*time.Hour)
			assert.Empty(t, f.restartStalled(t))
			f.control(t, stalled.SourceID, "resume")
			assert.Len(t, f.restartStalled(t), 1)
		})
	})
	t.Run("secretary disabled", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			f.stall(t, domain.ErrSecretaryExecutionUnknown)
			_, err := f.store.UpdateSecretarySettings(
				t.Context(),
				"owner",
				domain.SecretarySettingsUpdate{
					AgentID:          "secretary",
					ExpectedRevision: f.settings.Revision,
				},
			)
			require.NoError(t, err)
			f.age(t, 24*time.Hour)
			assert.Empty(t, f.restartStalled(t))
		})
	})
	t.Run("known failure with attempts left", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			f.stall(t, domain.ErrSecretaryExecutionFailed)
			f.age(t, 24*time.Hour)
			assert.Empty(t, f.restartStalled(t), "the ordinary bounded retry still owns this task")
		})
	})
	t.Run("running under a live lease", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			f.begin(t, f.prepare(t))
			assert.Empty(t, f.restartStalled(t))
		})
	})
}

func TestSecretaryAutoRestartGivenCanceledContextThenChangesNothing(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		f.stall(t, domain.ErrSecretaryExecutionUnknown)
		f.age(t, 24*time.Hour)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := f.store.(domain.SecretaryAutoRestartStore).RestartStalledSecretaryTasks(ctx, 0)
		require.Error(t, err)
		assert.Len(t, f.restartStalled(t), 1, "the stalled task must still be restartable")
	})
}

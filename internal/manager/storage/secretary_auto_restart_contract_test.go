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
		`UPDATE secretary_settings SET updated_at=updated_at-make_interval(secs=>$1)`,
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

// exhaustAttempts spends every dispatched attempt of one batch on rejected output.
func (f *secretaryLifecycleFixture) exhaustAttempts(t *testing.T) domain.SecretaryJob {
	t.Helper()
	var blocked domain.SecretaryJob
	for range domain.SecretaryMaxValidationRetries + 1 {
		blocked = f.stall(t, validationFailure())
		f.releaseBackoff(t)
	}
	return blocked
}

// exhaustConnection spends the whole connection retry budget without a prompt.
func (f *secretaryLifecycleFixture) exhaustConnection(t *testing.T) domain.SecretaryJob {
	t.Helper()
	var exhausted domain.SecretaryJob
	for range domain.SecretaryConnectionMaxRetries + 1 {
		exhausted = f.begin(t, f.prepare(t))
		require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), exhausted,
			exhausted.AttemptID, domain.ErrSecretaryNotDispatched))
		f.releaseConnectionBackoff(t)
	}
	require.Equal(t, "blocked", f.connectionState(t, exhausted).state)
	return exhausted
}

func (f *secretaryLifecycleFixture) restartStalled(t *testing.T) []domain.SecretaryTaskRestart {
	t.Helper()
	restarted, err := f.store.(domain.SecretaryAutoRestartStore).
		RestartStalledSecretaryTasks(t.Context(), 0)
	require.NoError(t, err)
	return restarted
}

func (f *secretaryLifecycleFixture) taskAction(
	t *testing.T,
	id, action string,
) domain.SecretaryTaskAction {
	t.Helper()
	detail, err := f.store.(domain.SecretaryTaskStore).
		GetSecretaryTask(t.Context(), "owner", id, domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	return domain.SecretaryTaskAction{
		Action:           action,
		ExpectedRevision: detail.Task.Revision,
		BatchID:          detail.Task.BatchID,
		AttemptID:        detail.Task.AttemptID,
		RetryEpoch:       detail.Task.RetryEpoch,
		SettingsRevision: detail.Task.SettingsRevision,
		ConfirmRestart:   true,
	}
}

func (f *secretaryLifecycleFixture) control(t *testing.T, id, action string) {
	t.Helper()
	_, err := f.store.(domain.SecretaryTaskStore).
		ControlSecretaryTask(t.Context(), "owner", id, f.taskAction(t, id, action))
	require.NoError(t, err)
}

// recheckWithoutResult is a manual recheck that learns nothing new: it claims
// the original attempt and releases it again.
func (f *secretaryLifecycleFixture) recheckWithoutResult(t *testing.T, id string) {
	t.Helper()
	tasks := f.store.(domain.SecretaryTaskStore)
	claimed, err := tasks.ClaimSecretaryTaskRecovery(
		t.Context(), "owner", id, f.taskAction(t, id, "recheck"), time.Minute)
	require.NoError(t, err)
	require.NoError(t, tasks.ReleaseSecretaryTaskRecovery(t.Context(), claimed))
}

func (f *secretaryLifecycleFixture) assertUnclaimable(t *testing.T, reason string) {
	t.Helper()
	jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	require.Empty(t, jobs, reason)
}

func TestSecretaryAutoRestartGivenUnknownOutcomeThenRestartsOnlyAfterItsCooldown(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		stalled := f.stall(t, domain.ErrSecretaryExecutionUnknown)
		assert.Empty(t, f.restartStalled(t), "a terminal proof may still arrive")
		f.age(t, domain.SecretaryAutoRestartDelay(0)-time.Minute)
		assert.Empty(t, f.restartStalled(t))
		f.assertUnclaimable(t, "the unknown outcome still blocks ordinary claims")

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

func TestSecretaryAutoRestartGivenExhaustedOutputRetriesThenGivesOneFreshBatch(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		blocked := f.exhaustAttempts(t)
		f.assertUnclaimable(t, "the exhausted batch must be blocked")
		assert.Empty(t, f.restartStalled(t))

		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, "blocked", restarted[0].PreviousState)
		assert.Equal(t, 1, restarted[0].Restarts)
		fresh := f.begin(t, f.prepare(t))
		assert.NotEqual(t, blocked.BatchID, fresh.BatchID)
		assert.Equal(
			t,
			1,
			f.attemptCount(t, fresh.BatchID),
			"a restart receives a new attempt budget",
		)
		require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), fresh, fresh.AttemptID,
			validationFailure()))
		require.NoError(t, f.store.FailSecretaryJob(t.Context(), fresh, validationFailure()))
		f.releaseBackoff(t)
		for range domain.SecretaryMaxValidationRetries {
			f.stall(t, validationFailure())
			f.releaseBackoff(t)
		}

		// Three more rejected outputs on the same input: stop spending the agent.
		f.age(t, 7*24*time.Hour)
		assert.Empty(t, f.restartStalled(t), "rejected output must not be retried a third time")
		f.assertUnclaimable(t, "the task now waits for its owner")
	})
}

func TestSecretaryAutoRestartGivenRepeatedStallsThenStopsAtItsBudgetUntilTheOwnerRetries(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		var stalled domain.SecretaryJob
		for restart := 1; restart <= domain.SecretaryAutoRestartMax; restart++ {
			f.stall(t, domain.ErrSecretaryExecutionUnknown)
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
		f.assertUnclaimable(t, "the task now waits for its owner")

		f.control(t, stalled.SourceID, "retry")
		f.stall(t, domain.ErrSecretaryExecutionUnknown)
		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, 1, restarted[0].Restarts, "an explicit owner retry starts a new budget")
	})
}

func TestSecretaryAutoRestartGivenProgressOrSettingsChangeThenRestoresTheBudget(t *testing.T) {
	t.Run("publication after the restart", func(t *testing.T) {
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
	})
	t.Run("settings changed by the owner", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			f.stall(t, domain.ErrSecretaryExecutionUnknown)
			f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
			require.Len(t, f.restartStalled(t), 1)

			f.age(t, time.Second)
			disabled, err := f.store.UpdateSecretarySettings(
				t.Context(),
				"owner",
				domain.SecretarySettingsUpdate{
					AgentID:          "secretary",
					ExpectedRevision: f.settings.Revision,
				},
			)
			require.NoError(t, err)
			_, err = f.store.UpdateSecretarySettings(
				t.Context(),
				"owner",
				domain.SecretarySettingsUpdate{
					AgentID: "secretary", Enabled: true, ExpectedRevision: disabled.Revision,
				},
			)
			require.NoError(t, err)

			f.stall(t, domain.ErrSecretaryExecutionUnknown)
			// A second restart in the same chain would have to wait an hour.
			f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
			restarted := f.restartStalled(t)
			require.Len(t, restarted, 1)
			assert.Equal(t, 1, restarted[0].Restarts)
		})
	})
}

func TestSecretaryAutoRestartGivenExhaustedConnectionRetriesAndNewMessageThenStillRestarts(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		exhausted := f.exhaustConnection(t)
		// A new visible message clears the blocked flag, yet claims keep skipping
		// the exhausted batch. Nothing else would ever set the flag again.
		f.addMessage(t)
		f.assertUnclaimable(t, "new input must not reopen an exhausted connection batch")
		require.False(t, f.connectionState(t, exhausted).blocked)
		assert.Empty(t, f.restartStalled(t))

		f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
		restarted := f.restartStalled(t)
		require.Len(t, restarted, 1)
		assert.Equal(t, "blocked", restarted[0].PreviousState)
		assert.Equal(t, 1, restarted[0].Restarts)
		fresh := f.begin(t, f.prepare(t))
		assert.NotEqual(t, exhausted.BatchID, fresh.BatchID)
		assert.Zero(
			t,
			f.connectionState(t, fresh).failures,
			"a restart starts a new connection budget",
		)
	})
}

func TestSecretaryAutoRestartGivenManualRecheckLeftTheTaskBlockedThenRecoversIt(t *testing.T) {
	t.Run("interrupted attempt is restarted", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			running := f.begin(t, f.prepare(t))
			f.expire(t)
			f.recheckWithoutResult(t, running.SourceID)
			f.assertUnclaimable(t, "the recheck leaves the job blocked behind a running batch")

			f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
			restarted := f.restartStalled(t)
			require.Len(t, restarted, 1)
			assert.Equal(t, "running", restarted[0].PreviousState)
			assert.False(t, restarted[0].Resumed)
			fresh := f.begin(t, f.prepare(t))
			assert.NotEqual(t, running.BatchID, fresh.BatchID)
		})
	})
	t.Run("saved output is released for publishing without a model call", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			saved := f.begin(t, f.prepare(t))
			require.NoError(
				t,
				f.store.SaveSecretaryAttemptOutput(t.Context(), saved, saved.AttemptID,
					"saved", domain.SecretaryToolReads{}),
			)
			f.expire(t)
			f.recheckWithoutResult(t, saved.SourceID)
			f.assertUnclaimable(t, "the recheck leaves the job blocked behind saved output")

			f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
			recovered := f.restartStalled(t)
			require.Len(t, recovered, 1)
			assert.Equal(t, domain.SecretaryTaskRestart{
				OwnerUserID: "owner", TaskID: saved.SourceID,
				PreviousState: "output_received", Resumed: true,
			}, recovered[0])
			assert.Empty(t, f.restartStalled(t))

			resumed := f.prepare(t)
			assert.Equal(t, saved.BatchID, resumed.BatchID, "the saved output must be kept")
			require.NotNil(t, resumed.Attempt)
			assert.Equal(t, "output_received", resumed.Attempt.State)
			assert.Equal(t, 1, f.attemptCount(t, saved.BatchID))
			_, err := f.store.PublishSecretaryBatch(t.Context(), resumed,
				secretaryNodesPublication(resumed, "decision"))
			require.NoError(t, err)
		})
	})
	t.Run("a recheck in progress is not interrupted", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			stalled := f.stall(t, domain.ErrSecretaryExecutionUnknown)
			f.age(t, domain.SecretaryAutoRestartDelay(0)+time.Minute)
			tasks := f.store.(domain.SecretaryTaskStore)
			claimed, err := tasks.ClaimSecretaryTaskRecovery(t.Context(), "owner", stalled.SourceID,
				f.taskAction(t, stalled.SourceID, "recheck"), time.Minute)
			require.NoError(t, err)
			assert.Empty(t, f.restartStalled(t), "the recheck lease must be respected")
			require.NoError(t, tasks.ReleaseSecretaryTaskRecovery(t.Context(), claimed))
			assert.Len(t, f.restartStalled(t), 1)
		})
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
	t.Run("rejected output followed by new input", func(t *testing.T) {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			blocked := f.exhaustAttempts(t)
			f.addMessage(t)
			f.age(t, 24*time.Hour)
			assert.Empty(t, f.restartStalled(t), "the next claim already builds a fresh batch")
			fresh := f.prepare(t)
			assert.NotEqual(t, blocked.BatchID, fresh.BatchID)
			assert.Equal(t, blocked.RetryEpoch, fresh.RetryEpoch)
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

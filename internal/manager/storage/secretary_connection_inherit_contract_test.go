package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (f *secretaryLifecycleFixture) editConnectionSource(t *testing.T, revision int) {
	t.Helper()
	text := fmt.Sprintf("Edited source version %d", revision)
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, "m1", text, 1)
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1'`, text)
	require.NoError(t, err)
}

func (f *secretaryLifecycleFixture) supersedeConnectionInput(t *testing.T, revision int) {
	t.Helper()
	f.editConnectionSource(t, revision)
	f.releaseConnectionBackoff(t)
	claimed := secretaryClaim(t, f.store)
	_, err := f.store.PrepareSecretaryBatch(t.Context(), claimed)
	require.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
	require.NoError(t, f.store.FailSecretaryJob(t.Context(), claimed, err))
	f.releaseConnectionBackoff(t)
}

func TestSecretaryConnectionGivenRepeatedSourceEditsThenKeepsTheAutomaticRetryBudget(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		for failure := 1; failure <= domain.SecretaryConnectionMaxRetries; failure++ {
			require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID,
				domain.ErrSecretaryNotDispatched))
			previous := job
			f.supersedeConnectionInput(t, failure)
			job = f.begin(t, f.prepare(t))
			assert.NotEqual(t, previous.BatchID, job.BatchID)
			assert.Greater(t, job.RetryEpoch, previous.RetryEpoch)
			assert.Equal(t, failure, f.connectionState(t, job).failures,
				"automatic fixed-input replacement must keep prior connection failures")
		}
		require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID,
			domain.ErrSecretaryNotDispatched))
		assert.True(t, f.connectionState(t, job).blocked)
		f.editConnectionSource(t, 99)
		f.releaseConnectionBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}

func TestSecretaryConnectionGivenManualRetryAfterAutomaticSupersedeThenStartsAFreshBudget(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID,
			domain.ErrSecretaryNotDispatched))
		f.supersedeConnectionInput(t, 1)
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		retried := f.begin(t, f.prepare(t))
		assert.Greater(t, retried.RetryEpoch, job.RetryEpoch+1)
		assert.Zero(t, f.connectionState(t, retried).failures)
	})
}

func TestSecretaryConnectionGivenChangedSettingsOrOwnerThenDoesNotInheritForeignRetryBudget(
	t *testing.T,
) {
	for _, change := range []string{"settings", "owner"} {
		t.Run(change, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				job := f.begin(t, f.prepare(t))
				require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID,
					domain.ErrSecretaryNotDispatched))
				f.supersedeConnectionInput(t, 1)
				owner, agentID := "owner", "source"
				if change == "owner" {
					owner, agentID = "other", "other"
					if f.memory != nil {
						agent := f.memory.agents["source"]
						agent.OwnerUserID = owner
						f.memory.agents["source"] = agent
					} else {
						_, err := f.postgres.db.ExecContext(t.Context(),
							`UPDATE agents SET owner_user_id=$1 WHERE agent_id='source'`, owner)
						require.NoError(t, err)
					}
				}
				settings, err := f.store.GetSecretarySettings(t.Context(), owner)
				require.NoError(t, err)
				_, err = f.store.UpdateSecretarySettings(
					t.Context(),
					owner,
					domain.SecretarySettingsUpdate{
						AgentID:          agentID,
						Enabled:          true,
						ExpectedRevision: settings.Revision,
					},
				)
				require.NoError(t, err)
				retried := f.begin(t, f.prepare(t))
				assert.Equal(t, owner, retried.OwnerUserID)
				assert.Zero(t, f.connectionState(t, retried).failures)
			})
		})
	}
}

func TestSecretaryConnectionGivenInheritedFailuresThenKeepsThreeFreshValidationAttempts(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		for range 3 {
			job := f.begin(t, f.prepare(t))
			require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID,
				domain.ErrSecretaryNotDispatched))
			f.releaseConnectionBackoff(t)
		}
		f.supersedeConnectionInput(t, 1)
		for failure := 1; failure <= 3; failure++ {
			job := f.begin(t, f.prepare(t))
			failureErr := domain.SecretaryOutputValidationError{
				Code: "schema",
				Err:  errors.New("invalid output"),
			}
			require.NoError(
				t,
				f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failureErr),
			)
			state := f.connectionState(t, job)
			assert.Equal(t, 3, state.failures)
			assert.Equal(t, failure == 3, state.blocked)
			require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, failureErr))
			f.releaseConnectionBackoff(t)
		}
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}

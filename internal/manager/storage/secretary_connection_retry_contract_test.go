package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryConnectionState struct {
	state, attempt string
	failures       int
	notDispatched  bool
	blocked        bool
	retryAfter     time.Time
	available      time.Time
	lease          string
	processed      int64
}

func (f *secretaryLifecycleFixture) connectionState(
	t *testing.T,
	job domain.SecretaryJob,
) secretaryConnectionState {
	t.Helper()
	var result secretaryConnectionState
	if f.memory != nil {
		batch := f.memory.secretaryState.executions[job.BatchID]
		queued := f.memory.secretaryState.jobs[job.SourceID]
		attempt := batch.attempts[len(batch.attempts)-1]
		result = secretaryConnectionState{state: batch.state, attempt: attempt.State,
			failures: batch.connectionFailures, notDispatched: attempt.NotDispatched,
			blocked: queued.executionBlocked, retryAfter: batch.retryAfter,
			available: queued.available, lease: queued.work.LeaseToken, processed: queued.processed}
		return result
	}
	require.NoError(
		t,
		f.postgres.db.QueryRowContext(t.Context(), `SELECT b.state,a.state,b.connection_failures,
 a.not_dispatched,j.execution_blocked,COALESCE(b.retry_after,'epoch'),j.available_at,j.lease_token,j.processed_revision
 FROM secretary_batches b JOIN secretary_attempts a ON a.attempt_id=b.current_attempt_id
 JOIN secretary_jobs j ON j.source_id=b.source_id WHERE b.batch_id=$1`, job.BatchID).Scan(
			&result.state, &result.attempt, &result.failures, &result.notDispatched, &result.blocked,
			&result.retryAfter, &result.available, &result.lease, &result.processed),
	)
	return result
}

func (f *secretaryLifecycleFixture) releaseConnectionBackoff(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		*f.now = f.now.Add(6 * time.Minute)
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`UPDATE secretary_batches SET retry_after=NOW()-INTERVAL '1 second';
 UPDATE secretary_jobs SET available_at=NOW()-INTERVAL '1 second',updated_at=NOW()-INTERVAL '1 minute'`,
	)
	require.NoError(t, err)
}

func TestSecretaryConnectionGivenUndispatchedFailureThenPersistsBackoffAndReleasesLease(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		before := time.Now().UTC()
		if f.memory != nil {
			before = *f.now
		}
		require.NoError(t, f.store.FailSecretaryAttempt(
			t.Context(),
			job,
			job.AttemptID,
			errors.Join(
				domain.ErrSecretaryNotDispatched,
				errors.New("agent tunnel not connected"),
			),
		))
		state := f.connectionState(t, job)
		assert.Equal(t, "pending", state.state)
		assert.Equal(t, "failed", state.attempt)
		assert.Equal(t, 1, state.failures)
		assert.True(t, state.notDispatched)
		assert.False(t, state.blocked)
		assert.Empty(t, state.lease)
		assert.WithinDuration(t, state.retryAfter, state.available, time.Microsecond)
		assert.WithinDuration(
			t,
			before.Add(domain.SecretaryConnectionRetryDelay(1, job.AttemptID)),
			state.retryAfter,
			time.Second,
		)
		assert.Zero(t, state.processed)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		f.restart()
		f.addMessage(t)
		jobs, err = f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs, "new visible messages must not bypass the durable batch deadline")
		f.releaseConnectionBackoff(t)
		restored := f.prepare(t)
		assert.Equal(t, job.BatchID, restored.BatchID)
		require.NotNil(t, restored.Attempt)
		assert.True(t, restored.Attempt.NotDispatched)
		assert.Len(
			t,
			restored.Messages,
			1,
			"the fixed input must not expand to include the new turn",
		)
		retried := f.begin(t, restored)
		assert.NotEqual(t, job.AttemptID, retried.AttemptID)
	})
}

func TestSecretaryConnectionGivenNineUndispatchedFailuresThenPausesAcrossNewTurnsUntilExplicitRetry(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		var previous domain.SecretaryJob
		for failure := 1; failure <= domain.SecretaryConnectionMaxRetries+1; failure++ {
			job := f.begin(t, f.prepare(t))
			if previous.BatchID != "" {
				require.Equal(t, previous.BatchID, job.BatchID)
			}
			require.NoError(
				t,
				f.store.FailSecretaryAttempt(
					t.Context(),
					job,
					job.AttemptID,
					domain.ErrSecretaryNotDispatched,
				),
			)
			state := f.connectionState(t, job)
			assert.Equal(t, failure, state.failures)
			assert.Equal(t, failure > domain.SecretaryConnectionMaxRetries, state.blocked)
			if failure <= domain.SecretaryConnectionMaxRetries {
				assert.Equal(t, "pending", state.state)
			} else {
				assert.Equal(t, "blocked", state.state)
			}
			previous = job
			f.releaseConnectionBackoff(t)
		}
		f.addMessage(t)
		f.restart()
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(
			t,
			jobs,
			"new input and restarts must not reopen an exhausted connection batch",
		)
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		retried := f.begin(t, f.prepare(t))
		assert.NotEqual(t, previous.BatchID, retried.BatchID)
		assert.Greater(t, retried.RetryEpoch, previous.RetryEpoch)
		assert.Equal(t, 0, f.connectionState(t, retried).failures)
	})
}

func TestSecretaryConnectionGivenPriorConnectionFailuresThenKeepsThreeOutputValidationAttempts(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		for range 4 {
			job := f.begin(t, f.prepare(t))
			require.NoError(
				t,
				f.store.FailSecretaryAttempt(
					t.Context(),
					job,
					job.AttemptID,
					domain.ErrSecretaryNotDispatched,
				),
			)
			f.releaseConnectionBackoff(t)
		}
		for failure := 1; failure <= 3; failure++ {
			job := f.begin(t, f.prepare(t))
			err := domain.SecretaryOutputValidationError{
				Code: "schema",
				Err:  errors.New("invalid output"),
			}
			require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, err))
			state := f.connectionState(t, job)
			assert.Equal(t, 4, state.failures)
			assert.False(t, state.notDispatched)
			assert.Equal(t, failure == 3, state.blocked)
			require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, err))
			f.releaseConnectionBackoff(t)
		}
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}

func TestSecretaryConnectionGivenUnknownOrPersistedOutputThenDoesNotTreatItAsUndispatched(
	t *testing.T,
) {
	for _, saved := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "unknown", true: "persisted_output"}[saved],
			func(t *testing.T) {
				secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
					job := f.begin(t, f.prepare(t))
					failure := errors.Join(
						domain.ErrSecretaryNotDispatched,
						domain.ErrSecretaryExecutionUnknown,
					)
					if saved {
						require.NoError(
							t,
							f.store.SaveSecretaryAttemptOutput(
								t.Context(),
								job,
								job.AttemptID,
								"stored output",
								domain.SecretaryToolReads{},
							),
						)
						failure = domain.ErrSecretaryNotDispatched
					}
					err := f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure)
					if saved {
						assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
					} else {
						require.NoError(t, err)
					}
					state := f.connectionState(t, job)
					assert.Zero(t, state.failures)
					assert.False(t, state.notDispatched)
					if saved {
						assert.Equal(t, "output_received", state.state)
					} else {
						assert.Equal(t, "unknown", state.state)
						assert.True(t, state.blocked)
					}
				})
			},
		)
	}
}

func TestSecretaryConnectionGivenDirectPrepareOrBeginThenEnforcesAuthoritativeRetryGate(
	t *testing.T,
) {
	for _, phase := range []string{"prepare", "begin"} {
		for _, exhausted := range []bool{false, true} {
			t.Run(
				phase+map[bool]string{false: "/deferred", true: "/exhausted"}[exhausted],
				func(t *testing.T) {
					secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
						job := f.prepare(t)
						state, failures := "pending", 1
						if exhausted {
							state, failures = "blocked", domain.SecretaryConnectionMaxRetries+1
						}
						if f.memory != nil {
							batch := f.memory.secretaryState.executions[job.BatchID]
							batch.state, batch.connectionFailures = state, failures
							batch.retryAfter = f.now.Add(time.Minute)
						} else {
							_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_batches SET state=$2,
 connection_failures=$3,retry_after=NOW()+INTERVAL '1 minute' WHERE batch_id=$1`, job.BatchID, state, failures)
							require.NoError(t, err)
						}
						var err error
						if phase == "prepare" {
							_, err = f.store.PrepareSecretaryBatch(t.Context(), job)
						} else {
							_, err = f.store.BeginSecretaryAttempt(t.Context(), job)
						}
						if exhausted {
							assert.ErrorIs(t, err, domain.ErrSecretaryExecutionBlocked)
						} else {
							assert.ErrorIs(t, err, domain.ErrSecretaryRetryDeferred)
						}
						active, err := f.store.SecretaryJobActive(t.Context(), job)
						require.NoError(t, err)
						assert.False(t, active, "retry gates must durably release the active lease")
					})
				},
			)
		}
	}
}

func TestSecretaryConnectionPostgresGivenFinalUpdateFailureThenRollsBackAttemptAndBatch(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	fixture := &secretaryLifecycleFixture{store: store, postgres: store}
	fixture.settings = secretaryEnable(t, store)
	job := fixture.begin(t, fixture.prepare(t))
	_, err := store.db.ExecContext(
		t.Context(),
		`CREATE FUNCTION reject_connection_retry() RETURNS trigger
 LANGUAGE plpgsql AS $$ BEGIN IF NEW.last_error<>'' THEN RAISE EXCEPTION 'injected retry persistence failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_connection_retry BEFORE UPDATE ON secretary_jobs FOR EACH ROW EXECUTE FUNCTION reject_connection_retry()`,
	)
	require.NoError(t, err)
	err = store.FailSecretaryAttempt(
		t.Context(),
		job,
		job.AttemptID,
		domain.ErrSecretaryNotDispatched,
	)
	require.ErrorContains(t, err, "injected retry persistence failure")
	state := fixture.connectionState(t, job)
	assert.Equal(t, "running", state.state)
	assert.Equal(t, "running", state.attempt)
	assert.False(t, state.notDispatched)
	assert.Zero(t, state.failures)
	assert.Equal(t, job.LeaseToken, state.lease)
}

func TestSecretaryConnectionGivenExistingPublishedSummaryThenFailurePreservesItAndCheckpoint(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		first := f.begin(t, f.prepare(t))
		require.NoError(t, f.store.SaveSecretaryAttemptOutput(t.Context(), first, first.AttemptID,
			"stored output", domain.SecretaryToolReads{}))
		versions, err := f.store.PublishSecretaryBatch(
			t.Context(),
			first,
			secretaryNodesPublication(first, "existing"),
		)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		f.addMessage(t)
		job := f.begin(t, f.prepare(t))
		before := f.connectionState(t, job)
		require.Positive(t, before.processed)
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(
				t.Context(),
				job,
				job.AttemptID,
				domain.ErrSecretaryNotDispatched,
			),
		)
		after := f.connectionState(t, job)
		assert.Equal(t, before.processed, after.processed)
		page, err := f.store.ListSecretaryNodeSummaries(
			t.Context(),
			job.OwnerUserID,
			job.SourceID,
			domain.SummaryListQuery{},
		)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, versions[0].SummaryID, page.Items[0].SummaryID)
		assert.Equal(t, versions[0].Revision, page.Items[0].Revision)
		assert.Equal(t, versions[0].Coverage, page.Items[0].Coverage)
		assert.Equal(t, versions[0].Body, page.Items[0].Body)
	})
}

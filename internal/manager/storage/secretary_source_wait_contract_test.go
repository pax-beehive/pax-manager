package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretarySourceWaitState struct {
	failure                        string
	processed, epoch, generation   int64
	jobAttempts, batches, attempts int
	blocked, leased                bool
}

func secretarySourceWaitObserved(
	t *testing.T,
	f *secretaryLifecycleFixture,
) secretarySourceWaitState {
	t.Helper()
	var state secretarySourceWaitState
	if f.memory != nil {
		job := f.memory.secretaryState.jobs["1"]
		state.failure, state.processed = job.lastError, job.processed
		state.epoch, state.generation = job.work.RetryEpoch, job.work.RetryGeneration
		state.blocked = job.executionBlocked
		state.leased = job.work.LeaseToken != "" && job.leaseUntil.After(f.memory.now())
		state.batches = len(f.memory.secretaryExecutionsLocked())
		for _, batch := range f.memory.secretaryExecutionsLocked() {
			state.attempts += len(batch.attempts)
		}
		return state
	}
	require.NoError(
		t,
		f.postgres.db.QueryRowContext(t.Context(), `SELECT last_error,processed_revision,retry_epoch,
 retry_generation,attempts,execution_blocked,lease_token<>'' AND lease_until>NOW(),
 (SELECT count(*) FROM secretary_batches),(SELECT count(*) FROM secretary_attempts)
 FROM secretary_jobs WHERE source_id=1`).
			Scan(&state.failure, &state.processed, &state.epoch,
				&state.generation, &state.jobAttempts, &state.blocked, &state.leased, &state.batches, &state.attempts),
	)
	return state
}

func secretarySourceWaitFailure(t *testing.T, f *secretaryLifecycleFixture, failure string) {
	t.Helper()
	if f.memory != nil {
		f.memory.secretaryState.jobs["1"].lastError = failure
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET last_error=$1 WHERE source_id=1`,
		failure,
	)
	require.NoError(t, err)
}

func TestSecretarySourceWaitGivenIncompleteInputThenPersistsReasonUntilSuccessfulPreparation(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		if f.memory != nil {
			message := f.memory.messages["m1"]
			message.Status, message.TurnID = "streaming", "turn_1"
			f.memory.messages["m1"] = message
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE messages SET status='streaming',turn_id='turn_1' WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		before := secretarySourceWaitObserved(t, f)
		for range 2 {
			job := secretaryClaim(t, f.store)
			_, err := f.store.PrepareSecretaryBatch(t.Context(), job)
			require.ErrorIs(t, err, domain.ErrSecretarySourceIncomplete)
			f.restart()
			waiting := secretaryReadTask(t, f)
			assert.Equal(t, "waiting_source", waiting.Task.ExecutionState)
			assert.Equal(t, "source_incomplete", waiting.Task.ErrorCode)
			assert.Equal(
				t,
				"Waiting for the source turn to finish before summarizing.",
				waiting.Task.ErrorMessage,
			)
			assert.Nil(t, waiting.Task.NextRetryAt)
			assert.False(t, waiting.Task.LeaseActive)
			assert.Empty(t, waiting.Task.BatchID)
			assert.Empty(t, waiting.Attempts)
			expected := before
			expected.failure = domain.ErrSecretarySourceIncomplete.Error()
			assert.Equal(
				t,
				expected,
				secretarySourceWaitObserved(t, f),
				"waiting must not consume an attempt or advance the checkpoint",
			)
			f.expire(t)
		}
		if f.memory != nil {
			f.memory.messages["done"] = Message{
				MessageID:   "done",
				AgentID:     "source",
				SessionID:   "session",
				TurnID:      "turn_1",
				SessionSeq:  2,
				MessageType: "turn_done",
				Status:      "complete",
			}
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages(message_id,agent_id,session_id,turn_id,source,direction,role,message_type,session_seq,status)
 VALUES('done','source','session','turn_1','test','inbound','assistant','turn_done',2,'complete')`)
			require.NoError(t, err)
		}
		job := secretaryClaim(t, f.store)
		assert.Equal(
			t,
			domain.ErrSecretarySourceIncomplete.Error(),
			secretarySourceWaitObserved(t, f).failure,
			"claiming alone must not erase the persisted waiting reason",
		)
		prepared, err := f.store.PrepareSecretaryBatch(t.Context(), job)
		require.NoError(t, err)
		assert.Empty(t, secretarySourceWaitObserved(t, f).failure)
		assert.Empty(t, secretaryReadTask(t, f).Task.ErrorCode)
		running := f.begin(t, prepared)
		assert.Equal(t, "running", secretaryReadTask(t, f).Task.ExecutionState)
		assert.Empty(t, secretarySourceWaitObserved(t, f).failure)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(t.Context(), running, running.AttemptID,
				"saved output", domain.SecretaryToolReads{}),
		)
		_, err = f.store.PublishSecretaryBatch(
			t.Context(),
			running,
			secretaryNodesPublication(running, "completed"),
		)
		require.NoError(t, err)
		completed := secretaryReadTask(t, f)
		assert.Equal(t, "completed", completed.Task.ExecutionState)
		assert.Empty(t, completed.Task.ErrorCode)
		assert.Empty(t, secretarySourceWaitObserved(t, f).failure)
		assert.Equal(t, 1, f.attemptCount(t, running.BatchID))
	})
}

func TestSecretarySourceWaitGivenSuccessfulPreparationThenClearsOnlyTheWaitingReason(t *testing.T) {
	for _, failure := range []string{domain.ErrSecretarySourceIncomplete.Error(), "another execution failure"} {
		t.Run(failure, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				claimed := secretaryClaim(t, f.store)
				secretarySourceWaitFailure(t, f, failure)
				expected := failure
				if failure == domain.ErrSecretarySourceIncomplete.Error() {
					expected = ""
				}
				job, err := f.store.PrepareSecretaryBatch(t.Context(), claimed)
				require.NoError(t, err)
				assert.Equal(t, expected, secretarySourceWaitObserved(t, f).failure)
				running := f.begin(t, job)
				assert.Equal(t, expected, secretarySourceWaitObserved(t, f).failure)
				assert.Equal(t, 1, f.attemptCount(t, running.BatchID))
			})
		})
	}
}

func TestSecretarySourceWaitGivenFencedPreparationOrBeginThenKeepsTheWaitingReason(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.prepare(t)
		secretarySourceWaitFailure(t, f, domain.ErrSecretarySourceIncomplete.Error())
		job.LeaseToken = "stale"
		_, err := f.store.PrepareSecretaryBatch(t.Context(), job)
		assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
		_, err = f.store.BeginSecretaryAttempt(t.Context(), job)
		assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
		assert.Equal(
			t,
			domain.ErrSecretarySourceIncomplete.Error(),
			secretarySourceWaitObserved(t, f).failure,
		)
		assert.Zero(t, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretarySourceWaitPostgresGivenReasonWriteFailureThenRollsBackPreparation(t *testing.T) {
	store := secretaryPostgresFixture(t)
	f := &secretaryLifecycleFixture{store: store, postgres: store}
	f.settings = secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	secretarySourceWaitFailure(t, f, domain.ErrSecretarySourceIncomplete.Error())
	before := secretarySourceWaitObserved(t, f)
	_, err := store.db.ExecContext(
		t.Context(),
		`CREATE FUNCTION reject_source_wait_clear() RETURNS trigger AS $$
 BEGIN IF OLD.last_error='secretary source turn is not complete' AND NEW.last_error='' THEN
 RAISE EXCEPTION 'source wait clear rejected'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql;
 CREATE TRIGGER source_wait_clear_failure BEFORE UPDATE ON secretary_jobs
 FOR EACH ROW EXECUTE FUNCTION reject_source_wait_clear()`,
	)
	require.NoError(t, err)
	_, err = store.PrepareSecretaryBatch(t.Context(), job)
	require.ErrorContains(t, err, "source wait clear rejected")
	assert.Equal(t, before, secretarySourceWaitObserved(t, f))
	_, err = store.db.ExecContext(
		t.Context(),
		`DROP TRIGGER source_wait_clear_failure ON secretary_jobs`,
	)
	require.NoError(t, err)
	_, err = store.PrepareSecretaryBatch(t.Context(), job)
	require.NoError(t, err)
	assert.Empty(t, secretarySourceWaitObserved(t, f).failure)
}

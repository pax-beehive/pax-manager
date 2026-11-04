package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryLifecycleStore interface {
	domain.SecretaryStore
	domain.SecretaryExecutionStore
	domain.SecretaryNodeStore
}

type secretaryLifecycleFixture struct {
	store    secretaryLifecycleStore
	memory   *MemoryStore
	postgres *PostgresStore
	now      *time.Time
	settings domain.SecretarySettings
}

func secretaryLifecycleFixtures(
	t *testing.T,
	scenario func(*testing.T, *secretaryLifecycleFixture),
) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		memory, now := secretaryMemoryFixture(t)
		fixture := &secretaryLifecycleFixture{store: memory, memory: memory, now: now}
		fixture.settings = secretaryEnable(t, fixture.store)
		scenario(t, fixture)
	})
	t.Run("postgres", func(t *testing.T) {
		postgres := secretaryPostgresFixture(t)
		fixture := &secretaryLifecycleFixture{store: postgres, postgres: postgres}
		fixture.settings = secretaryEnable(t, fixture.store)
		scenario(t, fixture)
	})
}

func (f *secretaryLifecycleFixture) prepare(t *testing.T) domain.SecretaryJob {
	t.Helper()
	job, err := f.store.PrepareSecretaryBatch(t.Context(), secretaryClaim(t, f.store))
	require.NoError(t, err)
	require.NotEmpty(t, job.BatchID)
	return job
}

func (f *secretaryLifecycleFixture) begin(
	t *testing.T,
	job domain.SecretaryJob,
) domain.SecretaryJob {
	t.Helper()
	attempt, err := f.store.BeginSecretaryAttempt(t.Context(), job)
	require.NoError(t, err)
	require.Equal(t, job.BatchID, attempt.BatchID)
	require.Equal(t, "running", attempt.State)
	job.AttemptID, job.RunID = attempt.ID, attempt.ID
	return job
}

func (f *secretaryLifecycleFixture) expire(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		*f.now = f.now.Add(2 * time.Minute)
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET lease_until=NOW()-INTERVAL '1 second', available_at=NOW()`,
	)
	require.NoError(t, err)
}

func (f *secretaryLifecycleFixture) releaseBackoff(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		*f.now = f.now.Add(6 * time.Second)
		return
	}
	releaseSecretaryBackoff(t, f.postgres)
}

func (f *secretaryLifecycleFixture) attemptCount(t *testing.T, batch string) int {
	t.Helper()
	if f.memory != nil {
		return len(f.memory.secretaryState.executions[batch].attempts)
	}
	var count int
	require.NoError(
		t,
		f.postgres.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_attempts WHERE batch_id=$1`, batch).
			Scan(&count),
	)
	return count
}

func (f *secretaryLifecycleFixture) addMessage(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, "m2", "A later turn", 2)
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq,status) VALUES('m2','source','session','test','inbound','user','chat',2,'complete');
    INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('m2',0,'text','A later turn')`,
	)
	require.NoError(t, err)
}

func (f *secretaryLifecycleFixture) restart() {
	if f.postgres != nil {
		f.postgres = NewPostgresStore(f.postgres.db, time.Now)
		f.store = f.postgres
	}
}

func TestSecretaryLifecycleGivenThreeFailedAttemptsThenBlocksFurtherClaimsUntilExplicitRetry(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		var batch, previous string
		for attemptNumber := 1; attemptNumber <= 3; attemptNumber++ {
			job := f.prepare(t)
			if batch == "" {
				batch = job.BatchID
			}
			require.Equal(
				t,
				batch,
				job.BatchID,
				"retry generation must not change fixed batch identity",
			)
			job = f.begin(t, job)
			assert.NotEqual(t, previous, job.AttemptID)
			previous = job.AttemptID
			require.NoError(
				t,
				f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, validationFailure()),
			)
			if attemptNumber == 3 {
				_, err := f.store.PrepareSecretaryBatch(t.Context(), job)
				assert.ErrorIs(t, err, domain.ErrSecretaryExecutionBlocked)
			}
			require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, validationFailure()))
			f.releaseBackoff(t)
		}
		for range 3 {
			jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
			require.NoError(t, err)
			assert.Empty(t, jobs)
		}
		assert.Equal(t, 3, f.attemptCount(t, batch))
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		fresh := f.prepare(t)
		assert.NotEqual(t, batch, fresh.BatchID)
		assert.Positive(t, fresh.RetryEpoch)
		fresh = f.begin(t, fresh)
		assert.Equal(t, 1, f.attemptCount(t, fresh.BatchID))
	})
}

func TestSecretaryLifecycleGivenAnUncertainRunningAttemptThenNeverAllocatesAnotherAttempt(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		_, err := f.store.BeginSecretaryAttempt(t.Context(), job)
		require.ErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
		require.NoError(
			t,
			f.store.FailSecretaryJob(t.Context(), job, domain.ErrSecretaryExecutionUnknown),
		)
		f.releaseBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretaryLifecycleGivenSavedOutputThenRestartRestoresExactOutputAndReadsWithoutNewAttempt(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		reads := domain.SecretaryToolReads{
			Messages: []domain.SecretaryReadMessage{
				{
					Ref:         secretaryReference(job.Messages[0]),
					Text:        "original",
					View:        "last",
					ReadReceipt: "fixture-receipt",
				},
			},
		}
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				`{"outputs":[]}`,
				reads,
			),
		)
		f.expire(t)
		f.restart()
		recovered := f.prepare(t)
		require.Equal(t, job.BatchID, recovered.BatchID)
		require.NotNil(t, recovered.Attempt)
		assert.Equal(t, "output_received", recovered.Attempt.State)
		assert.Equal(t, job.AttemptID, recovered.Attempt.ID)
		assert.Equal(t, `{"outputs":[]}`, recovered.Attempt.Output)
		assert.Equal(t, reads, recovered.Attempt.Reads)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
		results, err := f.store.PublishSecretaryBatch(
			t.Context(),
			recovered,
			secretaryNodesPublication(recovered, "decision"),
		)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretaryLifecycleGivenFencedOrExpiredExecutionThenRejectsLateOutputAndNewAttempts(
	t *testing.T,
) {
	for _, mode := range []string{"wrong lease", "expired lease", "disabled", "wrong attempt"} {
		t.Run(mode, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				job := f.begin(t, f.prepare(t))
				switch mode {
				case "wrong lease":
					job.LeaseToken = "old-lease"
				case "expired lease":
					f.expire(t)
				case "disabled":
					_, err := f.store.UpdateSecretarySettings(
						t.Context(),
						"owner",
						domain.SecretarySettingsUpdate{
							AgentID:          "secretary",
							ExpectedRevision: f.settings.Revision,
						},
					)
					require.NoError(t, err)
				case "wrong attempt":
					job.AttemptID = "not-current"
				}
				err := f.store.SaveSecretaryAttemptOutput(
					t.Context(),
					job,
					job.AttemptID,
					"late output",
					domain.SecretaryToolReads{},
				)
				assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
				if mode != "wrong attempt" {
					_, err = f.store.BeginSecretaryAttempt(t.Context(), job)
					assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
				}
				assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
			})
		})
	}
}

func TestSecretaryLifecycleGivenOutputReceivedThenRejectsDuplicateOutputAndWrongAttemptFailure(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"first",
				domain.SecretaryToolReads{},
			),
		)
		assert.ErrorIs(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"replacement",
				domain.SecretaryToolReads{},
			),
			domain.ErrSecretaryFenced,
		)
		assert.ErrorIs(
			t,
			f.store.FailSecretaryAttempt(t.Context(), job, "not-current", errors.New("offline")),
			domain.ErrSecretaryFenced,
		)
	})
}

func TestSecretaryLifecycleGivenCommittedBatchThenReplaysSameManifestAndRejectsChangedHash(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved",
				domain.SecretaryToolReads{},
			),
		)
		publication := secretaryNodesPublication(job, "one", "two")
		first, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
		require.NoError(t, err)
		f.restart()
		replay, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
		require.NoError(t, err)
		assert.Equal(t, first, replay)
		publication.Outputs[0].Request.Body = "Changed proposal"
		_, err = f.store.PublishSecretaryBatch(t.Context(), job, publication)
		assert.ErrorIs(t, err, domain.ErrConflict)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretaryLifecycleGivenNewInputDuringOutputRecoveryThenCommitsOnlyTheFixedBatch(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved",
				domain.SecretaryToolReads{},
			),
		)
		f.addMessage(t)
		f.expire(t)
		f.restart()
		recovered := f.prepare(t)
		require.Equal(
			t,
			job.BatchID,
			recovered.BatchID,
			"new source input must not discard already saved model output",
		)
		require.NotNil(t, recovered.Attempt)
		require.Len(t, recovered.Messages, 1)
		assert.Equal(t, job.ThroughRevision, recovered.ThroughRevision)
		_, err := f.store.PublishSecretaryBatch(
			t.Context(),
			recovered,
			secretaryNodesPublication(recovered, "one"),
		)
		require.NoError(t, err)
		next := f.prepare(t)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "m2", next.Messages[0].SourceID)
		assert.NotEqual(t, job.BatchID, next.BatchID)
	})
}

func TestSecretaryLifecycleGivenNoPreparedBatchThenRejectsAllAttemptMutations(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryClaim(t, f.store)
		job.BatchID = "unknown-batch"
		_, err := f.store.BeginSecretaryAttempt(t.Context(), job)
		require.Error(t, err)
		assert.Error(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				"unknown",
				"text",
				domain.SecretaryToolReads{},
			),
		)
		assert.Error(
			t,
			f.store.FailSecretaryAttempt(t.Context(), job, "unknown", errors.New("offline")),
		)
	})
}

func TestSecretaryLifecycleGivenOversizedOutputThenRejectsBeforePersistingIt(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		err := f.store.SaveSecretaryAttemptOutput(
			t.Context(),
			job,
			job.AttemptID,
			strings.Repeat("x", 1024*1024+1),
			domain.SecretaryToolReads{},
		)
		assert.Equal(t, "schema", domain.SecretaryValidationFailureCode(err))
		reads := domain.SecretaryToolReads{
			Messages: []domain.SecretaryReadMessage{
				{Text: strings.Repeat("x", domain.SecretaryMaxSourceBytes+1)},
			},
		}
		assert.ErrorIs(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"valid output",
				reads,
			),
			domain.ErrConflict,
		)
	})
}

func TestSecretaryLifecycleGivenFailureClassificationThenKeepsUnknownSeparateFromRetryableFailure(
	t *testing.T,
) {
	cases := []struct {
		name           string
		failure        error
		count          int
		attempt, batch string
	}{
		{"fenced", domain.ErrSecretaryFenced, 1, "canceled", "blocked"},
		{"unknown", domain.ErrSecretaryExecutionUnknown, 1, "unknown", "unknown"},
		{"invalid", validationFailure(), 1, "invalid", "pending"},
		{"exhausted invalid", validationFailure(), 3, "invalid", "blocked"},
		{"temporary", context.DeadlineExceeded, 1, "failed", "pending"},
		{"exhausted temporary", errors.New("offline"), 3, "failed", "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempt, batch := secretaryAttemptFailure(tc.failure, tc.count)
			assert.Equal(t, tc.attempt, attempt)
			assert.Equal(t, tc.batch, batch)
		})
	}
}

func TestSecretaryLifecycleGivenManualRetryDuringActiveAttemptThenPreservesBatchEpochAndLease(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		var epoch int64
		if f.memory != nil {
			epoch = f.memory.secretaryState.jobs[job.SourceID].work.RetryEpoch
		} else {
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT retry_epoch FROM secretary_jobs WHERE source_id=$1`, job.SourceID).Scan(&epoch))
		}
		assert.Equal(
			t,
			job.RetryEpoch,
			epoch,
			"an explicit retry must not replace an active execution's identity",
		)
		active, err := f.store.SecretaryJobActive(t.Context(), job)
		require.NoError(t, err)
		assert.True(t, active)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved active output",
				domain.SecretaryToolReads{},
			),
		)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretaryLifecycleGivenIncompleteSourceThenWaitsWithoutCreatingAnAttempt(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		if f.memory != nil {
			message := f.memory.messages["m1"]
			message.Status, message.TurnID = "streaming", "turn_1"
			f.memory.messages["m1"] = message
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE messages SET status='streaming',turn_id='turn_1' WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		job := secretaryClaim(t, f.store)
		_, err := f.store.PrepareSecretaryBatch(t.Context(), job)
		require.ErrorIs(t, err, domain.ErrSecretarySourceIncomplete)
		if f.memory != nil {
			assert.Empty(t, f.memory.secretaryState.executions)
		} else {
			var batches, attempts int
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_batches`).Scan(&batches))
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_attempts`).Scan(&attempts))
			assert.Zero(t, batches)
			assert.Zero(t, attempts)
		}
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
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
			_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages(message_id,agent_id,session_id,turn_id,source,direction,role,message_type,session_seq,status) VALUES('done','source','session','turn_1','test','inbound','assistant','turn_done',2,'complete')`)
			require.NoError(t, err)
		}
		f.expire(t)
		settled := f.begin(t, f.prepare(t))
		assert.Equal(t, 1, f.attemptCount(t, settled.BatchID))
	})
}

func TestSecretaryLifecycleGivenEquivalentFixedInputsThenIdentityIgnoresTransportAndRetryDetails(
	t *testing.T,
) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	job.Messages = append(
		job.Messages,
		domain.SearchDocument{
			SourceID:                "m2",
			ContentHash:             "second",
			CanonicalizationVersion: "visible-v1",
		},
	)
	job.RemovedMessageIDs = []string{"old-2", "old-1"}
	job.CurrentSummaries = []domain.SummaryVersion{
		{SummaryID: "s2", Revision: 2},
		{SummaryID: "s1", Revision: 3},
	}
	original := secretaryBatchID(job)
	reordered := job
	reordered.Messages = []domain.SearchDocument{job.Messages[1], job.Messages[0]}
	reordered.RemovedMessageIDs = []string{"old-1", "old-2"}
	reordered.CurrentSummaries = []domain.SummaryVersion{
		job.CurrentSummaries[1],
		job.CurrentSummaries[0],
	}
	reordered.LeaseToken, reordered.RunID, reordered.AttemptID = "new-lease", "new-run", "new-attempt"
	reordered.RetryGeneration++
	assert.Equal(t, original, secretaryBatchID(reordered))
	reordered.RetryEpoch++
	assert.NotEqual(t, original, secretaryBatchID(reordered))
	reordered = job
	reordered.SettingsRevision++
	assert.NotEqual(t, original, secretaryBatchID(reordered))
	assert.Equal(
		t,
		"old-2",
		job.RemovedMessageIDs[0],
		"identity calculation must not sort caller slices in place",
	)
	assert.Equal(t, "s2", job.CurrentSummaries[0].SummaryID)
}

func TestSecretaryLifecycleGivenPersistedInputThenRestoresCurrentFenceWithoutEphemeralAttempt(
	t *testing.T,
) {
	original := domain.SecretaryJob{
		SourceID:        "source",
		LeaseToken:      "old",
		AttemptID:       "attempt",
		Attempt:         &domain.SecretaryAttempt{ID: "attempt"},
		ThroughRevision: 7,
	}
	raw := secretaryBatchInput(original)
	restored, err := secretaryRestoreBatch(
		raw,
		domain.SecretaryJob{LeaseToken: "new", RetryGeneration: 2, ValidationFeedback: "evidence"},
	)
	require.NoError(t, err)
	assert.Equal(t, "new", restored.LeaseToken)
	assert.EqualValues(t, 7, restored.ThroughRevision)
	assert.EqualValues(t, 2, restored.RetryGeneration)
	assert.Equal(t, "evidence", restored.ValidationFeedback)
	assert.Nil(t, restored.Attempt)
	assert.Empty(t, restored.AttemptID)
	_, err = secretaryRestoreBatch([]byte(`{`), original)
	assert.Error(t, err)
}

func TestSecretaryLifecycleGivenPreparedBatchWithoutAttemptThenRejectsOutputFailureAndCommit(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.prepare(t)
		assert.Error(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				"missing",
				"text",
				domain.SecretaryToolReads{},
			),
		)
		assert.Error(
			t,
			f.store.FailSecretaryAttempt(t.Context(), job, "missing", errors.New("offline")),
		)
		_, err := f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "not-committed"),
		)
		assert.Error(t, err)
		assert.Zero(t, f.attemptCount(t, job.BatchID))
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, 1, status.PendingCount)
		assert.Nil(t, status.LastCompletedAt)
	})
}

func TestSecretaryLifecycleGivenFailedAttemptThenRejectsItsLateOutputAfterFreshAttemptStarts(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		firstID := job.AttemptID
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(t.Context(), job, firstID, errors.New("offline")),
		)
		fresh := f.begin(t, job)
		require.NotEqual(t, firstID, fresh.AttemptID)
		assert.ErrorIs(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				firstID,
				"late",
				domain.SecretaryToolReads{},
			),
			domain.ErrSecretaryFenced,
		)
		assert.ErrorIs(
			t,
			f.store.FailSecretaryAttempt(t.Context(), job, firstID, validationFailure()),
			domain.ErrSecretaryFenced,
		)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				fresh,
				fresh.AttemptID,
				"new output",
				domain.SecretaryToolReads{},
			),
		)
		assert.Equal(t, 2, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretaryLifecyclePostgresGivenCanceledContextThenCannotMutateExecution(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	job, err := store.PrepareSecretaryBatch(t.Context(), secretaryClaim(t, store))
	require.NoError(t, err)
	attempt, err := store.BeginSecretaryAttempt(t.Context(), job)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.PrepareSecretaryBatch(ctx, job)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = store.BeginSecretaryAttempt(ctx, job)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(
		t,
		store.SaveSecretaryAttemptOutput(
			ctx,
			job,
			attempt.ID,
			"not saved",
			domain.SecretaryToolReads{},
		),
		context.Canceled,
	)
	assert.ErrorIs(
		t,
		store.FailSecretaryAttempt(ctx, job, attempt.ID, errors.New("offline")),
		context.Canceled,
	)
	var state, output string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT state,output_text FROM secretary_attempts WHERE attempt_id=$1`, attempt.ID).
			Scan(&state, &output),
	)
	assert.Equal(t, "running", state)
	assert.Empty(t, output)
}

func TestSecretaryLifecycleMemoryGivenCanceledContextThenCannotMutateExecution(t *testing.T) {
	for _, operation := range []string{"prepare", "begin", "save", "fail"} {
		t.Run(operation, func(t *testing.T) {
			store, _ := secretaryMemoryFixture(t)
			secretaryEnable(t, store)
			job, err := store.PrepareSecretaryBatch(t.Context(), secretaryClaim(t, store))
			require.NoError(t, err)
			attempt, err := store.BeginSecretaryAttempt(t.Context(), job)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			switch operation {
			case "prepare":
				_, err = store.PrepareSecretaryBatch(ctx, job)
			case "begin":
				_, err = store.BeginSecretaryAttempt(ctx, job)
			case "save":
				err = store.SaveSecretaryAttemptOutput(
					ctx,
					job,
					attempt.ID,
					"not saved",
					domain.SecretaryToolReads{},
				)
			case "fail":
				err = store.FailSecretaryAttempt(ctx, job, attempt.ID, errors.New("offline"))
			}
			assert.ErrorIs(t, err, context.Canceled)
			saved := store.secretaryState.executions[job.BatchID].attempts[0]
			assert.Equal(t, "running", saved.State)
			assert.Empty(t, saved.Output)
		})
	}
}

func TestSecretaryLifecycleGivenUnknownOutcomeAndNewTurnThenDoesNotResendUnresolvedInput(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		f.addMessage(t)
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(
				t.Context(),
				job,
				job.AttemptID,
				domain.ErrSecretaryExecutionUnknown,
			),
		)
		require.NoError(
			t,
			f.store.FailSecretaryJob(t.Context(), job, domain.ErrSecretaryExecutionUnknown),
		)
		f.releaseBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(
			t,
			jobs,
			"new input must not authorize repeating an execution with an unknown outcome",
		)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
	})
}

func TestSecretaryLifecycleGivenSourceChangedBeforeCommitThenSupersedesBatchAndReadsFreshInput(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved",
				domain.SecretaryToolReads{},
			),
		)
		if f.memory != nil {
			part := f.memory.messageParts[messagePartKey{MessageID: "m1", Index: 0}]
			part.Text = "Corrected original"
			f.memory.messageParts[messagePartKey{MessageID: "m1", Index: 0}] = part
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE message_parts SET text='Corrected original' WHERE message_id='m1' AND part_index=0`)
			require.NoError(t, err)
		}
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		_, err := f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "outdated"),
		)
		require.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
		require.NoError(t, f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, err))
		require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, err))
		f.releaseBackoff(t)
		fresh := f.prepare(t)
		require.NotEqual(t, job.BatchID, fresh.BatchID)
		assert.Greater(t, fresh.RetryEpoch, job.RetryEpoch)
		require.Len(t, fresh.Messages, 1)
		assert.Equal(t, "Corrected original", fresh.Messages[0].Body)
		assert.Nil(t, fresh.Attempt)
		fresh = f.begin(t, fresh)
		assert.Equal(t, 1, f.attemptCount(t, fresh.BatchID))
	})
}

func TestSecretaryLifecycleGivenCommittedBatchAndRevokedSourceThenNeverReplaysSensitiveManifest(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved",
				domain.SecretaryToolReads{},
			),
		)
		publication := secretaryNodesPublication(job, "one")
		_, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
		require.NoError(t, err)
		if f.memory != nil {
			source := f.memory.agents["source"]
			source.OwnerUserID = "other"
			f.memory.agents["source"] = source
		} else {
			_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`)
			require.NoError(t, err)
		}
		results, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
		require.Error(t, err)
		assert.Empty(t, results)
	})
}

func TestSecretaryLifecyclePostgresGivenMalformedPersistedInputOrReadsThenFailsWithoutReplacingAttempt(
	t *testing.T,
) {
	for _, corrupted := range []string{"input", "reads"} {
		t.Run(corrupted, func(t *testing.T) {
			store := secretaryPostgresFixture(t)
			secretaryEnable(t, store)
			job, err := store.PrepareSecretaryBatch(t.Context(), secretaryClaim(t, store))
			require.NoError(t, err)
			attempt, err := store.BeginSecretaryAttempt(t.Context(), job)
			require.NoError(t, err)
			if corrupted == "input" {
				_, err = store.db.ExecContext(
					t.Context(),
					`UPDATE secretary_batches SET input_json='[]' WHERE batch_id=$1`,
					job.BatchID,
				)
			} else {
				_, err = store.db.ExecContext(t.Context(), `UPDATE secretary_attempts SET reads_json='[]' WHERE attempt_id=$1`, attempt.ID)
			}
			require.NoError(t, err)
			_, err = store.PrepareSecretaryBatch(t.Context(), job)
			require.Error(t, err)
			var count int
			require.NoError(
				t,
				store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_attempts WHERE batch_id=$1`, job.BatchID).
					Scan(&count),
			)
			assert.Equal(t, 1, count)
		})
	}
}

func TestSecretaryLifecycleGivenRunningAttemptWithoutSavedOutputThenCannotPublishOrAdvanceCheckpoint(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		_, err := f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "premature"),
		)
		assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, 1, status.PendingCount)
		assert.Nil(t, status.LastCompletedAt)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"now durable",
				domain.SecretaryToolReads{},
			),
		)
		results, err := f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "premature"),
		)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.EqualValues(t, 1, results[0].Revision)
	})
}

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryTerminalWakeStore interface {
	WakeSecretaryExecutionRecovery(context.Context, domain.SecretaryJob) error
}

func TestSecretaryTerminalRetryGivenKnownFailureDuringRecheckThenRetriesOnlyTwiceAndNeverCheckpoints(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		original := secretaryTaskUnknown(t, f, "transport did not confirm a terminal")
		tasks := secretaryTasksStore(t, f)
		observed := secretaryReadTask(t, f).Task
		recovered, err := tasks.ClaimSecretaryTaskRecovery(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(observed, "recheck"),
			time.Minute,
		)
		require.NoError(t, err)
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(
				t.Context(),
				recovered,
				recovered.AttemptID,
				domain.ErrSecretaryExecutionFailed,
			),
		)
		require.NoError(t, tasks.ReleaseSecretaryTaskRecovery(t.Context(), recovered))
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs, "a known failure must retain a short retry delay after recheck")
		for attempt := 2; attempt <= 3; attempt++ {
			if attempt == 3 && f.memory != nil {
				*f.now = f.now.Add(5 * time.Second)
			}
			f.releaseBackoff(t)
			next := f.prepare(t)
			require.Equal(t, original.BatchID, next.BatchID)
			require.NotNil(t, next.Attempt)
			assert.Equal(t, "failed", next.Attempt.State)
			assert.Empty(t, next.Attempt.Output)
			next = f.begin(t, next)
			require.NoError(
				t,
				f.store.FailSecretaryAttempt(
					t.Context(),
					next,
					next.AttemptID,
					domain.ErrSecretaryExecutionFailed,
				),
			)
			require.NoError(
				t,
				f.store.FailSecretaryJob(t.Context(), next, domain.ErrSecretaryExecutionFailed),
			)
			if attempt == 2 {
				state := f.connectionState(t, next)
				now := time.Now()
				if f.memory != nil {
					now = *f.now
				}
				assert.Greater(
					t,
					state.available.Sub(now),
					9*time.Second,
					"FailJob must preserve the doubled deadline",
				)
			}
		}
		f.releaseBackoff(t)
		jobs, err = f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		detail := secretaryReadTask(t, f)
		assert.Equal(t, "blocked", detail.Task.ExecutionState)
		assert.Equal(t, 3, f.attemptCount(t, original.BatchID))
		for _, attempt := range detail.Attempts {
			assert.False(t, attempt.HasOutput)
		}
		state := f.connectionState(t, original)
		assert.Zero(t, state.processed)
	})
}

func secretaryTerminalProof(t *testing.T, f *secretaryLifecycleFixture, job domain.SecretaryJob) {
	t.Helper()
	request := domain.Message{
		MessageID:   domain.SecretaryExecutionRequestMessageID(job),
		OwnerUserID: job.OwnerUserID,
		AgentID:     job.AgentID,
		SessionID:   secretaryExecutionSessionID(job.OwnerUserID, job.AttemptID),
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		MessageType: "secretary_execution_request",
		Status:      "prepared",
		Role:        "system",
	}
	// The execution turn suffix is the same deterministic suffix as the session.
	request.TurnID = "turn_secretary_" + request.SessionID[len(domain.SecretaryInternalSessionPrefix):]
	correlation := domain.SecretaryExecutionCorrelation{
		Version:   1,
		Method:    "session/prompt",
		RequestID: json.RawMessage(`12`),
		Binding: &domain.SecretaryExecutionBinding{
			SourceID:         job.SourceID,
			BatchID:          job.BatchID,
			AttemptID:        job.AttemptID,
			SettingsRevision: job.SettingsRevision,
			RetryGeneration:  job.RetryGeneration,
			RetryEpoch:       job.RetryEpoch,
		},
	}
	raw, err := json.Marshal(correlation)
	require.NoError(t, err)
	request.RawJSON = raw
	terminal := request
	terminal.MessageID = domain.SecretaryExecutionTerminalMessageID(request)
	terminal.MessageType, terminal.Status, terminal.Direction = "secretary_execution_failed", "failed", domain.MessageDirectionAgentToUser
	correlation.Failure, correlation.Origin = "cancelled", "reliable_inbox"
	raw, err = json.Marshal(correlation)
	require.NoError(t, err)
	terminal.RawJSON = raw
	require.True(t, domain.SecretaryExecutionTerminalMatches(request, terminal, job))
	if f.memory != nil {
		f.memory.secretaryLocked().internal[sessionKey(job.AgentID, request.SessionID)] = true
		f.memory.messages[request.MessageID], f.memory.messages[terminal.MessageID] = request, terminal
		return
	}
	_, err = f.postgres.db.ExecContext(
		t.Context(),
		`INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`,
		job.AgentID,
		request.SessionID,
		job.OwnerUserID,
	)
	require.NoError(t, err)
	require.NoError(t, f.postgres.UpsertMessage(t.Context(), &request))
	require.NoError(t, f.postgres.UpsertMessage(t.Context(), &terminal))
}

func TestSecretaryTerminalWakeGivenEitherTerminalRaceOrderingThenRecoversOriginalAttemptOnly(
	t *testing.T,
) {
	for _, timing := range []string{"before_unknown", "after_unknown"} {
		t.Run(timing, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				wake, ok := f.store.(secretaryTerminalWakeStore)
				require.True(t, ok)
				job := f.begin(t, f.prepare(t))
				if timing == "before_unknown" {
					secretaryTerminalProof(t, f, job)
					require.NoError(t, wake.WakeSecretaryExecutionRecovery(t.Context(), job))
				}
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
				if timing == "after_unknown" {
					secretaryTerminalProof(t, f, job)
					require.NoError(t, wake.WakeSecretaryExecutionRecovery(t.Context(), job))
				}
				f.releaseBackoff(t)
				restored := f.prepare(t)
				assert.Equal(t, job.BatchID, restored.BatchID)
				assert.Equal(t, job.AttemptID, restored.AttemptID)
				require.NotNil(t, restored.Attempt)
				assert.Equal(t, "unknown", restored.Attempt.State)
				assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
			})
		})
	}
}

func TestSecretaryTerminalWakeGivenUnconfirmedOrStaleEvidenceThenNeverUnblocks(t *testing.T) {
	for _, scenario := range []string{"missing_proof", "missing_terminal", "missing_internal_mapping", "untrusted_terminal", "source_archived", "source_owner_changed", "selected_owner_changed", "invalid_input", "other_owner", "other_agent", "other_batch", "other_attempt", "other_epoch", "other_generation", "other_settings", "disabled", "paused", "exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				wake, ok := f.store.(secretaryTerminalWakeStore)
				require.True(t, ok)
				original := secretaryTaskUnknown(t, f, "unconfirmed")
				job := original
				if scenario != "missing_proof" {
					secretaryTerminalProof(t, f, job)
				}
				mutateSecretaryTerminalProof(t, f, scenario, job)
				mutateSecretaryTerminalIdentity(t, f, scenario, &job)
				require.NoError(t, wake.WakeSecretaryExecutionRecovery(t.Context(), job))
				f.releaseBackoff(t)
				jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
				require.NoError(t, err)
				assert.Empty(t, jobs)
				assert.Equal(t, 1, f.attemptCount(t, original.BatchID))
			})
		})
	}
}

func TestSecretaryTerminalRetryGivenPriorValidationThenRecheckPreservesExecutionGeneration(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		first := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(t.Context(), first, first.AttemptID, validationFailure()),
		)
		require.NoError(t, f.store.FailSecretaryJob(t.Context(), first, validationFailure()))
		f.releaseBackoff(t)
		original := secretaryTaskUnknown(t, f, "response not yet received")
		require.Positive(t, original.RetryGeneration)
		task := secretaryReadTask(t, f).Task
		store := secretaryTasksStore(t, f)
		recovered, err := store.ClaimSecretaryTaskRecovery(
			t.Context(),
			"owner",
			"1",
			secretaryTaskObserved(task, "recheck"),
			time.Minute,
		)
		require.NoError(t, err)
		assert.Equal(t, original.RetryGeneration, recovered.RetryGeneration)
		assert.Equal(t, original.AttemptID, recovered.AttemptID)
		require.NoError(t, store.ReleaseSecretaryTaskRecovery(t.Context(), recovered))
	})
}

func TestSecretaryTerminalRetryGivenChangedAuthorityThenKnownFailureCannotMutateOldAttempt(
	t *testing.T,
) {
	for _, scenario := range []string{"settings", "retry_epoch"} {
		t.Run(scenario, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				original := secretaryTaskUnknown(t, f, "unconfirmed")
				store := secretaryTasksStore(t, f)
				task := secretaryReadTask(t, f).Task
				recovered, err := store.ClaimSecretaryTaskRecovery(
					t.Context(),
					"owner",
					"1",
					secretaryTaskObserved(task, "recheck"),
					time.Minute,
				)
				require.NoError(t, err)
				if scenario == "settings" {
					_, err = f.store.UpdateSecretarySettings(
						t.Context(),
						"owner",
						domain.SecretarySettingsUpdate{
							AgentID:          "secretary",
							Enabled:          true,
							ExpectedRevision: f.settings.Revision,
						},
					)
					require.NoError(t, err)
				} else {
					f.expire(t)
					observed := secretaryReadTask(t, f).Task
					action := secretaryTaskObserved(observed, "retry")
					action.ConfirmRestart = true
					_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", action)
					require.NoError(t, err)
				}
				err = f.store.FailSecretaryAttempt(
					t.Context(),
					recovered,
					recovered.AttemptID,
					domain.ErrSecretaryExecutionFailed,
				)
				assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
				assert.Equal(t, 1, f.attemptCount(t, original.BatchID))
			})
		})
	}
}

func TestSecretaryTerminalWakeGivenCanceledContextThenDoesNotTouchMemoryState(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(
		t,
		store.WakeSecretaryExecutionRecovery(ctx, domain.SecretaryJob{}),
		context.Canceled,
	)
}

func TestSecretaryTerminalRetryDelayGivenActualAttemptThenDoublesWithinThreeAttemptBudget(
	t *testing.T,
) {
	for _, test := range []struct {
		attempt int
		delay   time.Duration
	}{{0, 5 * time.Second}, {1, 5 * time.Second}, {2, 10 * time.Second}, {3, 10 * time.Second}, {20, 10 * time.Second}} {
		assert.Equal(t, test.delay, secretaryKnownFailureRetryDelay(test.attempt))
	}
}

func mutateSecretaryTerminalProof(
	t *testing.T,
	f *secretaryLifecycleFixture,
	scenario string,
	job domain.SecretaryJob,
) {
	t.Helper()
	switch scenario {
	case "missing_terminal", "untrusted_terminal":
		requestID := domain.SecretaryExecutionRequestMessageID(job)
		if f.memory != nil {
			id := domain.SecretaryExecutionTerminalMessageID(f.memory.messages[requestID])
			if scenario == "missing_terminal" {
				delete(f.memory.messages, id)
			} else {
				terminal := f.memory.messages[id]
				terminal.RawJSON = json.RawMessage(`{"version":1}`)
				f.memory.messages[id] = terminal
			}
		} else {
			query := `DELETE FROM messages WHERE message_type='secretary_execution_failed'`
			if scenario == "untrusted_terminal" {
				query = `UPDATE messages SET raw_json='{"version":1}' WHERE message_type='secretary_execution_failed'`
			}
			_, err := f.postgres.db.ExecContext(t.Context(), query)
			require.NoError(t, err)
		}
	case "missing_internal_mapping":
		if f.memory != nil {
			delete(
				f.memory.secretaryLocked().internal,
				sessionKey(
					job.AgentID,
					secretaryExecutionSessionID(job.OwnerUserID, job.AttemptID),
				),
			)
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `DELETE FROM secretary_internal_sessions`)
			require.NoError(t, err)
		}
	case "source_archived":
		if f.memory != nil {
			session := f.memory.sessions[sessionKey("source", "session")]
			session.ArchivedAt = f.now
			f.memory.sessions[sessionKey("source", "session")] = session
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE agent_sessions SET archived_at=NOW() WHERE agent_id='source'`)
			require.NoError(t, err)
		}
	case "source_owner_changed", "selected_owner_changed":
		agent := "source"
		if scenario == "selected_owner_changed" {
			agent = "secretary"
		}
		if f.memory != nil {
			record := f.memory.agents[agent]
			record.OwnerUserID = "other"
			f.memory.agents[agent] = record
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE agents SET owner_user_id='other' WHERE agent_id=$1`, agent)
			require.NoError(t, err)
		}
	case "invalid_input":
		if f.memory != nil {
			f.memory.secretaryState.executions[job.BatchID].input = []byte(`{}`)
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_batches SET input_json='{}' WHERE batch_id=$1`, job.BatchID)
			require.NoError(t, err)
		}
	}
}

func mutateSecretaryTerminalIdentity(
	t *testing.T,
	f *secretaryLifecycleFixture,
	scenario string,
	current *domain.SecretaryJob,
) {
	t.Helper()
	job := *current
	switch scenario {
	case "other_owner":
		job.OwnerUserID = "other"
	case "other_agent":
		job.AgentID = "other"
	case "other_batch":
		job.BatchID = "other"
	case "other_attempt":
		job.AttemptID = "other"
	case "other_epoch":
		job.RetryEpoch++
	case "other_generation":
		job.RetryGeneration++
	case "other_settings":
		job.SettingsRevision++
	case "disabled":
		_, err := f.store.UpdateSecretarySettings(
			t.Context(),
			"owner",
			domain.SecretarySettingsUpdate{
				ExpectedRevision: f.settings.Revision,
				AgentID:          "secretary",
				Enabled:          false,
			},
		)
		require.NoError(t, err)
	case "paused":
		observed := secretaryReadTask(t, f).Task
		_, err := secretaryTasksStore(
			t,
			f,
		).ControlSecretaryTask(t.Context(), "owner", "1", secretaryTaskObserved(observed, "pause"))
		require.NoError(t, err)
	case "exhausted":
		if f.memory != nil {
			f.memory.secretaryState.executions[job.BatchID].state = "blocked"
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_batches SET state='blocked' WHERE batch_id=$1`, job.BatchID)
			require.NoError(t, err)
		}
	}
	*current = job
}
func TestSecretaryTerminalRetryGivenConflictingFailureSignalsThenUnknownDominates(t *testing.T) {
	failure := errors.Join(domain.ErrSecretaryExecutionFailed, domain.ErrSecretaryExecutionUnknown)
	state, batch := secretaryAttemptFailure(failure, 1)
	assert.Equal(t, "unknown", state)
	assert.Equal(t, "unknown", batch)
	assert.False(t, secretaryKnownExecutionFailure(failure))
}

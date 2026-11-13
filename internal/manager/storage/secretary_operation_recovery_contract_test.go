package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryFailedGuidedOperation(
	t *testing.T,
	f *secretaryLifecycleFixture,
	scope, instruction string,
) domain.SecretaryOperation {
	t.Helper()
	operation := secretaryOperationDraft(t, f, "guided_summary", scope)
	_, err := secretaryOperationStore(
		f,
	).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID,
		domain.SecretaryOperationSubmit{
			RequestID:        "run:" + operation.OperationID,
			ExpectedRevision: operation.Revision,
			RunInstruction:   instruction,
			ConfirmReplace:   true,
		})
	require.NoError(t, err)
	work := f.begin(t, f.prepare(t))
	require.NoError(
		t,
		f.store.FailSecretaryAttempt(
			t.Context(),
			work,
			work.AttemptID,
			domain.ErrSecretaryExecutionUnknown,
		),
	)
	require.NoError(
		t,
		f.store.FailSecretaryJob(t.Context(), work, domain.ErrSecretaryExecutionUnknown),
	)
	failed, err := secretaryOperationStore(
		f,
	).GetSecretaryOperation(t.Context(), "owner", operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "needs_attention", failed.Status)
	assert.Equal(t, "outcome_unknown", failed.Error)
	assert.Equal(t, instruction, failed.Instructions.Run)
	return failed
}

func secretaryRetryOperationTask(t *testing.T, f *secretaryLifecycleFixture) {
	t.Helper()
	store := f.store.(domain.SecretaryTaskStore)
	detail, err := store.GetSecretaryTask(t.Context(), "owner", "1", domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	task := detail.Task
	_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", domain.SecretaryTaskAction{
		Action: "retry", ExpectedRevision: task.Revision, BatchID: task.BatchID, AttemptID: task.AttemptID,
		RetryEpoch: task.RetryEpoch, SettingsRevision: task.SettingsRevision, ConfirmRestart: true,
	})
	require.NoError(t, err)
}

func TestSecretaryOperationsGivenFailedGuidanceThenPassiveChangesCannotReplaceItWithDefaultWork(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		failed := secretaryFailedGuidedOperation(t, f, "pending", "Keep only material outcomes.")
		secretaryOperationMessage(t, f, "m2", "A later independent change", 2)
		_, err := f.store.UpdateSecretarySettings(
			t.Context(),
			"owner",
			domain.SecretarySettingsUpdate{
				AgentID: "secretary", Enabled: true, ExpectedRevision: f.settings.Revision},
		)
		require.NoError(t, err)
		guidance := f.store.(domain.SecretaryGuidanceStore)
		_, err = guidance.UpdateSecretaryGuidance(
			t.Context(),
			"owner",
			domain.SecretaryGuidanceUser,
			"",
			domain.SecretaryGuidanceUpdate{
				Instruction: "A new default must not replace the failed operation.",
			},
		)
		require.NoError(t, err)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		stillFailed, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", failed.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "needs_attention", stillFailed.Status)
		secretaryRetryOperationTask(t, f)
		retry := secretaryClaim(t, f.store)
		assert.Equal(t, failed.OperationID, retry.OperationID)
		assert.Equal(t, failed.Instructions, retry.Instructions)
		require.Len(t, retry.Messages, 1)
		assert.Equal(t, "m1", retry.Messages[0].SourceID)
	})
}

func TestSecretaryOperationsGivenFailedHistoricalReprocessThenBulkRetryPreservesScopeAndInstructions(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		manual := secretaryOperationDraft(t, f, "human_summary", "pending")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", manual.OperationID, secretaryHumanInput(manual))
		require.NoError(t, err)
		failed := secretaryFailedGuidedOperation(t, f, "all", "Reconsider the existing conclusion.")
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		queued, err := secretaryOperationStore(
			f,
		).GetSecretaryOperation(t.Context(), "owner", failed.OperationID)
		require.NoError(t, err)
		assert.Equal(t, "queued", queued.Status)
		work := f.prepare(t)
		assert.Equal(t, failed.OperationID, work.OperationID)
		assert.Equal(t, failed.Instructions, work.Instructions)
		assert.Nil(t, work.Attempt, "an explicit restart must not restore the old unknown attempt")
	})
}

func TestSecretaryOperationsGivenUserReplacementThenOldFailedInstructionsCannotBeResurrected(
	t *testing.T,
) {
	for _, replacement := range []string{"human_summary", "guided_summary"} {
		t.Run(replacement, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				old := secretaryFailedGuidedOperation(t, f, "pending", "The old requirement.")
				if replacement == "human_summary" {
					draft := secretaryOperationDraft(t, f, replacement, "all")
					input := secretaryHumanInput(draft)
					input.ConfirmReplace = true
					_, err := secretaryOperationStore(
						f,
					).SubmitSecretaryOperation(t.Context(), "owner", draft.OperationID, input)
					require.NoError(t, err)
					secretaryOperationMessage(t, f, "m2", "A later conclusion", 2)
					require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
					work := secretaryClaim(t, f.store)
					assert.Empty(t, work.OperationID)
					assert.Empty(t, work.Instructions.Run)
					require.Len(t, work.Messages, 1)
					assert.Equal(t, "m2", work.Messages[0].SourceID)
				} else {
					latest := secretaryFailedGuidedOperation(t, f, "all", "The replacement requirement.")
					secretaryRetryOperationTask(t, f)
					work := secretaryClaim(t, f.store)
					assert.Equal(t, latest.OperationID, work.OperationID)
					assert.Equal(t, latest.Instructions, work.Instructions)
				}
				superseded, err := secretaryOperationStore(
					f,
				).GetSecretaryOperation(t.Context(), "owner", old.OperationID)
				require.NoError(t, err)
				assert.Equal(t, "superseded", superseded.Status)
			})
		})
	}
}

func TestSecretaryOperationsGivenPausedSessionThenManualCompletionKeepsTheUsersPause(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryOperationMessage(t, f, "m2", "A second conclusion", 2)
		store := f.store.(domain.SecretaryTaskStore)
		detail, err := store.GetSecretaryTask(
			t.Context(),
			"owner",
			"1",
			domain.SecretaryTaskQuery{},
		)
		require.NoError(t, err)
		task := detail.Task
		_, err = store.ControlSecretaryTask(t.Context(), "owner", "1", domain.SecretaryTaskAction{
			Action: "pause", ExpectedRevision: task.Revision, BatchID: task.BatchID, AttemptID: task.AttemptID,
			RetryEpoch: task.RetryEpoch, SettingsRevision: task.SettingsRevision,
		})
		require.NoError(t, err)
		operation := secretaryOperationDraft(t, f, "human_summary", "all")
		input := secretaryHumanInput(operation)
		input.MessageIDs = []string{"m2"}
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, input)
		require.NoError(t, err)
		detail, err = store.GetSecretaryTask(t.Context(), "owner", "1", domain.SecretaryTaskQuery{})
		require.NoError(t, err)
		assert.True(t, detail.Task.Paused)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})
}

func TestSecretaryOperationsGivenSourceOwnerChangesThenForeignOperationsCannotBlockOrLeakInstructions(
	t *testing.T,
) {
	for _, state := range []string{"queued", "needs_attention"} {
		t.Run(state, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				var old domain.SecretaryOperation
				if state == "needs_attention" {
					old = secretaryFailedGuidedOperation(
						t,
						f,
						"pending",
						"Private old owner instruction.",
					)
				} else {
					old = secretaryOperationDraft(t, f, "guided_summary", "pending")
					_, err := secretaryOperationStore(f).SubmitSecretaryOperation(t.Context(), "owner", old.OperationID, domain.SecretaryOperationSubmit{
						RequestID: "old-owner-guidance", ExpectedRevision: old.Revision, RunInstruction: "Private old owner instruction."})
					require.NoError(t, err)
				}
				if f.memory != nil {
					agent := f.memory.agents["source"]
					agent.OwnerUserID = "other"
					f.memory.agents["source"] = agent
				} else {
					_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`)
					require.NoError(t, err)
				}
				_, err := f.store.UpdateSecretarySettings(
					t.Context(),
					"other",
					domain.SecretarySettingsUpdate{AgentID: "other", Enabled: true},
				)
				require.NoError(t, err)
				_, err = f.store.(domain.SecretaryGuidanceStore).UpdateSecretaryGuidance(
					t.Context(),
					"other",
					domain.SecretaryGuidanceUser,
					"",
					domain.SecretaryGuidanceUpdate{Instruction: "New owner preferences."},
				)
				require.NoError(t, err)
				if state == "needs_attention" {
					require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
					var oldStatus string
					if f.memory != nil {
						oldStatus = f.memory.secretaryState.operations[old.OperationID].value.Status
					} else {
						require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT status FROM secretary_operations WHERE operation_id=$1`, old.OperationID).Scan(&oldStatus))
					}
					assert.NotEqual(
						t,
						"queued",
						oldStatus,
						"the former owner cannot requeue work for a transferred source",
					)
				}
				jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
				work := jobs[0]
				assert.Equal(t, "other", work.OwnerUserID)
				assert.Empty(t, work.OperationID)
				require.NotNil(t, work.Instructions)
				assert.Equal(t, "other", work.Instructions.User.OwnerUserID)
				assert.Equal(t, "New owner preferences.", work.Instructions.User.Instruction)
				assert.Empty(t, work.Instructions.Run)
				for _, reader := range []string{"owner", "other"} {
					_, readErr := secretaryOperationStore(
						f,
					).GetSecretaryOperation(t.Context(), reader, old.OperationID)
					assert.ErrorIs(t, readErr, ErrNotFound)
				}
				require.NoError(
					t,
					f.store.FailSecretaryJob(
						t.Context(),
						work,
						errors.New("unused claim released"),
					),
				)
				fresh, err := secretaryOperationStore(
					f,
				).PrepareSecretaryOperation(t.Context(), "other", domain.SecretaryOperationPrepare{SourceID: "1", Kind: "guided_summary", Scope: "pending"})
				require.NoError(t, err)
				queued, err := secretaryOperationStore(
					f,
				).SubmitSecretaryOperation(t.Context(), "other", fresh.OperationID, domain.SecretaryOperationSubmit{
					RequestID: "new-owner-guidance", ExpectedRevision: fresh.Revision, RunInstruction: "The new owner requirement."})
				require.NoError(t, err)
				assert.Equal(t, "queued", queued.Status)
				assert.Equal(t, "other", queued.Instructions.User.OwnerUserID)
			})
		})
	}
}

func TestSecretaryOperationsGivenQueuedMessageMovesSessionThenItBlocksInsteadOfLoopingAsRemoval(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		operation := secretaryOperationDraft(t, f, "guided_summary", "pending")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, domain.SecretaryOperationSubmit{
			RequestID: "before-move", ExpectedRevision: operation.Revision, RunInstruction: "Summarize this exact session."})
		require.NoError(t, err)
		if f.memory != nil {
			f.memory.sessions[sessionKey("source", "moved")] = AgentSession{
				ID:            2,
				AgentID:       "source",
				SessionID:     "moved",
				RuntimeStatus: domain.RuntimeStatusIdle,
			}
			message := f.memory.messages["m1"]
			message.SessionID = "moved"
			f.memory.messages["m1"] = message
		} else {
			_, err = f.postgres.db.ExecContext(t.Context(), `INSERT INTO agent_sessions(agent_id,session_id,runtime_status) VALUES('source','moved','idle'); UPDATE messages SET session_id='moved' WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 10, time.Minute, 0)
		require.NoError(t, err)
		for _, job := range jobs {
			assert.NotEqual(
				t,
				operation.OperationID,
				job.OperationID,
				"moved targets must not become endless removal-only operation batches",
			)
		}
		var status, code string
		if f.memory != nil {
			stored := f.memory.secretaryState.operations[operation.OperationID].value
			status, code = stored.Status, stored.Error
		} else {
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT status,operation_json->>'error' FROM secretary_operations WHERE operation_id=$1`, operation.OperationID).Scan(&status, &code))
		}
		assert.Equal(t, "needs_attention", status)
		assert.Equal(t, "source_changed", code)
	})
}

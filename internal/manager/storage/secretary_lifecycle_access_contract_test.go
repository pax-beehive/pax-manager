package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryConfidentialMixedBody = "Mixed summary containing a confidential external fact."

func (f *secretaryLifecycleFixture) externalSource(t *testing.T) domain.MessageVersionRef {
	t.Helper()
	if f.memory != nil {
		f.memory.agents["related_agent"] = Agent{AgentID: "related_agent", OwnerUserID: "owner"}
		f.memory.sessions[sessionKey("related_agent", "related_session")] = AgentSession{
			ID:            2,
			AgentID:       "related_agent",
			SessionID:     "related_session",
			RuntimeStatus: domain.RuntimeStatusIdle,
		}
		secretaryMemoryMessage(f.memory, "external", "Confidential external fact", 1)
		message := f.memory.messages["external"]
		message.AgentID, message.SessionID = "related_agent", "related_session"
		f.memory.messages["external"] = message
	} else {
		_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO agents(agent_id,owner_user_id,hostname,os,api_key_hash) VALUES('related_agent','owner','host','linux','related-key');
        INSERT INTO agent_sessions(agent_id,session_id) VALUES('related_agent','related_session');
        INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,status,session_seq) VALUES('external','related_agent','related_session','test','inbound','user','chat','complete',1);
        INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('external',0,'text','Confidential external fact');`)
		require.NoError(t, err)
	}
	require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
	f.deferExternalSource(t)
	return summaryTestRef("external", "Confidential external fact")
}

func (f *secretaryLifecycleFixture) deferExternalSource(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		f.memory.secretaryState.jobs["2"].available = f.memory.now().Add(time.Hour)
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET available_at='infinity' WHERE source_agent_id='related_agent'`,
	)
	require.NoError(t, err)
}

func (f *secretaryLifecycleFixture) revokeExternalSource(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		agent := f.memory.agents["related_agent"]
		agent.OwnerUserID = "other"
		f.memory.agents["related_agent"] = agent
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`UPDATE agents SET owner_user_id='other' WHERE agent_id='related_agent'`,
	)
	require.NoError(t, err)
}

func (f *secretaryLifecycleFixture) deleteOriginal(t *testing.T) {
	t.Helper()
	if f.memory != nil {
		delete(f.memory.messages, "m1")
		delete(f.memory.messageParts, messagePartKey{MessageID: "m1", Index: 0})
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
	require.NoError(t, err)
}

func (f *secretaryLifecycleFixture) publishMixedSummary(t *testing.T) domain.SummaryVersion {
	t.Helper()
	external := f.externalSource(t)
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
	job.AllowedRelatedMessages = []domain.MessageVersionRef{external}
	publication := secretaryNodesPublication(job, "mixed")
	publication.Outputs[0].Request.Coverage = append(
		publication.Outputs[0].Request.Coverage,
		external,
	)
	publication.Outputs[0].Request.Body = secretaryConfidentialMixedBody
	results, err := f.store.PublishSecretaryBatch(t.Context(), job, publication)
	require.NoError(t, err)
	require.Len(t, results, 1)
	return results[0]
}

func TestSecretaryLifecycleAccessGivenCachedMixedInputThenDeletedOrRevokedEvidenceCannotReachAgentOnRestart(
	t *testing.T,
) {
	for _, change := range []string{"original deleted", "external revoked"} {
		t.Run(change, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				f.publishMixedSummary(t)
				f.addMessage(t)
				fixed := f.prepare(t)
				require.Len(t, fixed.CurrentSummaries, 1)
				require.Equal(t, secretaryConfidentialMixedBody, fixed.CurrentSummaries[0].Body)
				if change == "original deleted" {
					f.deleteOriginal(t)
				} else {
					f.revokeExternalSource(t)
				}
				require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
				f.expire(t)
				f.restart()
				f.deferExternalSource(t)
				claimed := secretaryClaim(t, f.store)
				recovered, err := f.store.PrepareSecretaryBatch(t.Context(), claimed)
				require.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
				raw, encodeErr := json.Marshal(recovered)
				require.NoError(t, encodeErr)
				assert.NotContains(t, string(raw), secretaryConfidentialMixedBody)
				var state string
				if f.memory != nil {
					state = f.memory.secretaryState.executions[fixed.BatchID].state
				} else {
					require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT state FROM secretary_batches WHERE batch_id=$1`, fixed.BatchID).Scan(&state))
				}
				assert.Equal(t, "superseded", state)
			})
		})
	}
}

func TestSecretaryLifecycleAccessGivenExternalOwnerRevokedThenQueuesOriginRepairWithoutManualInvalidation(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		version := f.publishMixedSummary(t)
		f.revokeExternalSource(t)
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		f.deferExternalSource(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		require.Len(
			t,
			jobs,
			1,
			"revoking an external source must schedule its origin's safe repair",
		)
		assert.Equal(t, "session", jobs[0].SourceSessionID)
		assert.Empty(t, jobs[0].CurrentSummaries)
		assert.Contains(
			t,
			jobs[0].InvalidatedSummaries,
			domain.SummaryRef{SummaryID: version.SummaryID, Revision: version.Revision},
		)
	})
}

func TestSecretaryLifecycleAccessGivenSavedOutputWhoseOnlyInputWasDeletedThenClaimStillResolvesOldBatch(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		fixed := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				fixed,
				fixed.AttemptID,
				"saved",
				domain.SecretaryToolReads{},
			),
		)
		f.deleteOriginal(t)
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		f.expire(t)
		f.restart()
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "an empty new delta must not skip recovery of saved model output")
		assert.Empty(t, jobs[0].Messages)
		recovered, err := f.store.PrepareSecretaryBatch(t.Context(), jobs[0])
		require.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
		assert.Empty(t, recovered.Messages)
		var state, attempt string
		if f.memory != nil {
			batch := f.memory.secretaryState.executions[fixed.BatchID]
			state, attempt = batch.state, batch.attempts[0].State
			assert.Zero(t, f.memory.secretaryState.jobs[fixed.SourceID].processed)
		} else {
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT b.state,a.state FROM secretary_batches b JOIN secretary_attempts a ON a.attempt_id=b.current_attempt_id WHERE b.batch_id=$1`, fixed.BatchID).Scan(&state, &attempt))
			var processed int64
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT processed_revision FROM secretary_jobs WHERE source_id=$1`, fixed.SourceID).Scan(&processed))
			assert.Zero(t, processed)
		}
		assert.Equal(t, "superseded", state)
		assert.Equal(t, "superseded", attempt)
	})
}

func TestSecretaryLifecycleAccessGivenCorruptedStoredIdentityThenRejectsWithoutReturningCachedBody(
	t *testing.T,
) {
	for _, field := range []string{"OwnerUserID", "SourceID", "SourceAgentID", "SourceSessionID", "AgentID", "SettingsRevision", "RetryEpoch", "BatchID"} {
		t.Run(field, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				fixed := f.prepare(t)
				var raw []byte
				if f.memory != nil {
					raw = f.memory.secretaryState.executions[fixed.BatchID].input
				} else {
					require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT input_json FROM secretary_batches WHERE batch_id=$1`, fixed.BatchID).Scan(&raw))
				}
				var input map[string]any
				require.NoError(t, json.Unmarshal(raw, &input))
				input[field] = "foreign"
				if field == "SettingsRevision" || field == "RetryEpoch" {
					input[field] = float64(99)
				}
				input["SourceMetadata"] = map[string]any{"Title": "Confidential cached title"}
				changed, err := json.Marshal(input)
				require.NoError(t, err)
				if f.memory != nil {
					f.memory.secretaryState.executions[fixed.BatchID].input = changed
				} else {
					_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_batches SET input_json=$2 WHERE batch_id=$1`, fixed.BatchID, string(changed))
					require.NoError(t, err)
				}
				recovered, err := f.store.PrepareSecretaryBatch(t.Context(), fixed)
				require.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
				assert.Empty(t, recovered.SourceMetadata.Title)
				assert.Empty(t, recovered.Messages)
			})
		})
	}
}

func TestSecretaryLifecycleAccessGivenPromptValidationBeforeAnyAttemptThenBlocksEvenWithOriginalClaim(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		claimed := secretaryClaim(t, f.store)
		fixed, err := f.store.PrepareSecretaryBatch(t.Context(), claimed)
		require.NoError(t, err)
		require.NotEmpty(t, fixed.BatchID)
		require.Empty(t, claimed.BatchID)
		failure := domain.SecretaryOutputValidationError{Code: "schema", Err: assert.AnError}
		require.NoError(t, f.store.FailSecretaryJob(t.Context(), claimed, failure))
		f.releaseBackoff(t)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		assert.Zero(t, f.attemptCount(t, fixed.BatchID))
		require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
		retried := f.prepare(t)
		assert.NotEqual(t, fixed.BatchID, retried.BatchID)
	})
}

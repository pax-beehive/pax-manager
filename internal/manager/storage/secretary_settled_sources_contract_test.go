package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretarySettledSource(t *testing.T, f *secretaryLifecycleFixture, status string) {
	t.Helper()
	if f.memory != nil {
		old := f.memory.now().Add(-time.Minute)
		message := f.memory.messages["m1"]
		message.Status, message.TurnID = status, "turn_1"
		message.CreatedAt, message.UpdatedAt = old, old
		f.memory.messages["m1"] = message
		for key, part := range f.memory.messageParts {
			if key.MessageID == "m1" {
				part.CreatedAt, part.UpdatedAt = old, old
				f.memory.messageParts[key] = part
			}
		}
		session := f.memory.sessions[sessionKey("source", "session")]
		session.UpdatedAt = old
		f.memory.sessions[sessionKey("source", "session")] = session
		return
	}
	_, err := f.postgres.db.ExecContext(
		t.Context(),
		`UPDATE messages SET status=$1,turn_id='turn_1',
 created_at=NOW()-INTERVAL '1 minute',updated_at=NOW()-INTERVAL '1 minute' WHERE message_id='m1'`,
		status,
	)
	require.NoError(t, err)
	_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE message_parts
 SET created_at=NOW()-INTERVAL '1 minute',updated_at=NOW()-INTERVAL '1 minute' WHERE message_id='m1';
 UPDATE agent_sessions SET updated_at=NOW()-INTERVAL '1 minute' WHERE agent_id='source'`)
	require.NoError(t, err)
}

func TestSecretarySettledSourceGivenOldIdleTextWithoutTerminalThenPublishesItsExactVersion(
	t *testing.T,
) {
	for _, status := range []string{"sent", "received", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				secretarySettledSource(t, f, status)
				job := secretaryNodesReadyJob(t, f.store)
				results, err := f.store.PublishSecretaryBatch(t.Context(), job,
					secretaryNodesPublication(job, "stable-content"))
				require.NoError(t, err)
				require.Len(t, results, 1)
				require.Len(t, results[0].Coverage, 1)
				ref := results[0].Coverage[0]
				assert.Equal(t, "m1", ref.MessageID)
				assert.Equal(t, domain.SearchContentHash("original"), ref.ContentHash)
				observed := secretarySourceWaitObserved(t, f)
				assert.Positive(t, observed.processed)
				assert.Empty(t, observed.failure)
				if f.memory != nil {
					assert.Equal(t, status, f.memory.messages["m1"].Status)
					assert.Len(t, f.memory.messages, 1)
				} else {
					var actual string
					var terminals int
					require.NoError(t, f.postgres.db.QueryRowContext(t.Context(),
						`SELECT status,(SELECT count(*) FROM messages WHERE message_type IN ('end_turn','turn_done'))
 FROM messages WHERE message_id='m1'`).Scan(&actual, &terminals))
					assert.Equal(t, status, actual)
					assert.Zero(t, terminals)
				}
			})
		})
	}
}

func TestSecretarySettledSourceGivenFreshPartsOrStreamingStateThenWaitsWithoutModelAttempt(
	t *testing.T,
) {
	for _, change := range []string{"message", "part", "second part", "streaming", "unknown metadata"} {
		t.Run(change, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				secretarySettledSource(t, f, "received")
				secretaryChangeSettledSource(t, f, change)
				require.NoError(t, f.store.RetrySecretaryJobs(t.Context(), "owner"))
				job := secretaryClaim(t, f.store)
				_, err := f.store.PrepareSecretaryBatch(t.Context(), job)
				require.ErrorIs(t, err, domain.ErrSecretarySourceIncomplete)
				state := secretarySourceWaitObserved(t, f)
				assert.Zero(t, state.processed)
				assert.Zero(t, state.batches)
				assert.Zero(t, state.attempts)
			})
		})
	}
}

func secretaryChangeSettledSource(t *testing.T, f *secretaryLifecycleFixture, change string) {
	t.Helper()
	if f.memory != nil {
		switch change {
		case "message", "streaming":
			message := f.memory.messages["m1"]
			if change == "streaming" {
				message.Status = "streaming"
			} else {
				message.UpdatedAt = f.memory.now()
			}
			f.memory.messages["m1"] = message
		case "part", "second part":
			key := messagePartKey{MessageID: "m1", Index: 0}
			part := f.memory.messageParts[key]
			if change == "second part" {
				key.Index, part.PartIndex = 1, 1
				part.Text = " added"
			}
			part.UpdatedAt = f.memory.now()
			f.memory.messageParts[key] = part
		case "idle transition":
			session := f.memory.sessions[sessionKey("source", "session")]
			session.UpdatedAt = f.memory.now()
			f.memory.sessions[sessionKey("source", "session")] = session
		case "unknown metadata":
			session := f.memory.sessions[sessionKey("source", "session")]
			session.Metadata = runtimeMetadata(
				nil,
				domain.SessionRuntimeState{Lifecycle: "unknown"},
			)
			f.memory.sessions[sessionKey("source", "session")] = session
		default:
			session := f.memory.sessions[sessionKey("source", "session")]
			session.RuntimeStatus = change
			f.memory.sessions[sessionKey("source", "session")] = session
		}
		return
	}
	statements := map[string]string{
		"message":         `UPDATE messages SET updated_at=NOW() WHERE message_id='m1'`,
		"streaming":       `UPDATE messages SET status='streaming' WHERE message_id='m1'`,
		"part":            `UPDATE message_parts SET updated_at=NOW() WHERE message_id='m1'`,
		"second part":     `INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('m1',1,'text',' added')`,
		"idle transition": `UPDATE agent_sessions SET updated_at=NOW() WHERE agent_id='source'`,
		"unknown metadata": `UPDATE agent_sessions
 SET metadata='{"runtime_state":{"lifecycle":"unknown"}}' WHERE agent_id='source'`,
	}
	statement, exists := statements[change]
	if exists {
		_, err := f.postgres.db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(),
		`UPDATE agent_sessions SET runtime_status=$1 WHERE agent_id='source'`, change)
	require.NoError(t, err)
}

func TestSecretarySettledSourceGivenLatePartsAfterPublicationThenRevisesAndPreservesOldSnapshot(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretarySettledSource(t, f, "received")
		job := secretaryNodesReadyJob(t, f.store)
		first, err := f.store.PublishSecretaryBatch(t.Context(), job,
			secretaryNodesPublication(job, "stable-content"))
		require.NoError(t, err)
		require.Len(t, first, 1)
		secretaryChangeSettledSource(t, f, "second part")
		secretarySettledSource(t, f, "received")
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		next := secretaryNodesReadyJob(t, f.store)
		require.Len(t, next.Messages, 1)
		assert.Equal(t, "original added", next.Messages[0].Body)
		publication := secretaryNodesPublication(next, "stable-content")
		publication.Outputs[0].Request.SummaryID = first[0].SummaryID
		publication.Outputs[0].Request.ExpectedRevision = first[0].Revision
		publication.Outcomes[0].SummaryKeys = []string{first[0].SummaryID}
		second, err := f.store.PublishSecretaryBatch(t.Context(), next, publication)
		require.NoError(t, err)
		require.Len(t, second, 1)
		assert.Equal(t, first[0].SummaryID, second[0].SummaryID)
		assert.EqualValues(t, 2, second[0].Revision)
		reader, ok := f.store.(domain.SummaryStore)
		require.True(t, ok)
		frozen, err := reader.ReadMessageVersion(t.Context(), "owner", first[0].Coverage[0])
		require.NoError(t, err)
		assert.Equal(t, "original", frozen.Body)
		assert.NotEqual(t, first[0].Coverage[0].ContentHash, second[0].Coverage[0].ContentHash)
	})
}

func TestSecretarySettledSourceGivenUnrelatedSessionMetadataUpdateThenDoesNotDelayStableText(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretarySettledSource(t, f, "received")
		secretaryChangeSettledSource(t, f, "idle transition")
		job := f.prepare(t)
		assert.NotEmpty(t, job.BatchID)
	})
}

func TestSecretaryCompletionGivenEarlierCanceledTerminalThenFindsLaterReliableCompletion(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretarySettledSource(t, f, "streaming")
		if f.memory != nil {
			for seq, status := range map[int64]string{2: "canceled", 3: "complete"} {
				id := status + "-terminal"
				f.memory.messages[id] = Message{
					MessageID: id, AgentID: "source", SessionID: "session", TurnID: "turn_1",
					SessionSeq: seq, MessageType: "turn_done", Status: status,
				}
			}
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,message_type,turn_id,session_seq,status) VALUES
 ('canceled-terminal','source','session','test','inbound','turn_done','turn_1',2,'canceled'),
 ('complete-terminal','source','session','test','inbound','turn_done','turn_1',3,'complete')`)
			require.NoError(t, err)
		}
		job := f.prepare(t)
		assert.NotEmpty(t, job.BatchID)
	})
}

func TestSecretarySettledSourceGivenRuntimeResumesBeforePublicationThenRejectsWithoutCheckpoint(
	t *testing.T,
) {
	for _, runtime := range []string{"running", "waiting_approval", "unknown"} {
		t.Run(runtime, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				secretarySettledSource(t, f, "received")
				job := secretaryNodesReadyJob(t, f.store)
				secretaryChangeSettledSource(t, f, runtime)
				_, err := f.store.PublishSecretaryBatch(t.Context(), job,
					secretaryNodesPublication(job, "not-stable"))
				require.ErrorIs(t, err, domain.ErrSecretarySourceIncomplete)
				assert.Zero(t, secretarySourceWaitObserved(t, f).processed)
			})
		})
	}
}

func TestSecretarySettledSourceGivenPartsChangeAfterReadThenRejectsOldOutput(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretarySettledSource(t, f, "received")
		job := secretaryNodesReadyJob(t, f.store)
		secretaryChangeSettledSource(t, f, "second part")
		_, err := f.store.PublishSecretaryBatch(t.Context(), job,
			secretaryNodesPublication(job, "outdated"))
		require.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
		assert.Zero(t, secretarySourceWaitObserved(t, f).processed)
	})
}

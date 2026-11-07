package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryPendingGivenRawOnlyChangesThenDoesNotQueueProcessedContent(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryClaim(t, f.store)
		_, err := f.store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
		require.NoError(t, err)
		if f.memory != nil {
			queued := f.memory.secretaryLocked().jobs[job.SourceID]
			queued.desired++
			queued.changes["m1"] = queued.desired
		} else {
			_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE messages SET updated_at=NOW() WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Zero(t, status.PendingCount)
		assert.Empty(t, status.ExecutionState)
		if f.memory != nil {
			queued := f.memory.secretaryLocked().jobs[job.SourceID]
			assert.Greater(
				t,
				queued.desired,
				queued.processed,
				"status reads must not consume dirty revisions",
			)
		} else {
			var dirty bool
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT desired_revision>processed_revision FROM secretary_jobs WHERE source_id=$1`, job.SourceID).Scan(&dirty))
			assert.True(t, dirty)
		}
		if f.memory != nil {
			f.memory.messageParts[messagePartKey{MessageID: "m1", Index: 1}] = MessagePart{
				MessageID: "m1", PartIndex: 1, PartType: "text", Text: " appended",
			}
			require.NoError(t, f.memory.BackfillSecretaryJobs(t.Context()))
		} else {
			_, err = f.postgres.db.ExecContext(t.Context(), `INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('m1',1,'text',' appended')`)
			require.NoError(t, err)
		}
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(
			t,
			1,
			status.PendingCount,
			"new text in another part is still one conversation",
		)
	})
}

func TestSecretaryPendingGivenInvisibleSourceThenDoesNotQueueUnreadBody(t *testing.T) {
	for _, mode := range []string{"archived", "internal", "registered_internal", "owner", "hidden", "replacement", "tool"} {
		t.Run(mode, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				if f.memory != nil {
					session := f.memory.sessions[sessionKey("source", "session")]
					message := f.memory.messages["m1"]
					switch mode {
					case "archived":
						session.ArchivedAt = f.now
					case "internal":
						session.Source = domain.SecretaryInternalSource
					case "registered_internal":
						f.memory.secretaryLocked().internal[sessionKey("source", "session")] = true
					case "owner":
						agent := f.memory.agents["source"]
						agent.OwnerUserID = "other"
						f.memory.agents["source"] = agent
					case "hidden":
						message.RawJSON = []byte(`{"params":{"update":{"channel":"ANALYSIS"}}}`)
					case "replacement":
						f.memory.messages["display"] = Message{
							MessageID:       "display",
							AgentID:         "source",
							SessionID:       "session",
							Role:            "assistant",
							MessageType:     domain.MessageTypePaxArtifact,
							ParentMessageID: "m1",
						}
					case "tool":
						message.Role = "tool"
					}
					f.memory.sessions[sessionKey("source", "session")] = session
					f.memory.messages["m1"] = message
				} else {
					queries := map[string]string{
						"archived":            `UPDATE agent_sessions SET archived_at=NOW()`,
						"internal":            `UPDATE agent_sessions SET source='secretary_internal'`,
						"registered_internal": `INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES('source','session','owner')`,
						"owner":               `UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`,
						"hidden":              `UPDATE messages SET raw_json='{"params":{"update":{"channel":"ANALYSIS"}}}'::jsonb WHERE message_id='m1'`,
						"replacement":         `INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,session_seq) VALUES('display','source','session','test','inbound','assistant','pax:artifact','m1',2)`,
						"tool":                `UPDATE messages SET role='tool' WHERE message_id='m1'`,
					}
					_, err := f.postgres.db.ExecContext(t.Context(), queries[mode])
					require.NoError(t, err)
				}
				status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
				require.NoError(t, err)
				assert.Zero(t, status.PendingCount)
				assert.Empty(t, status.ExecutionState)
			})
		})
	}
}

func TestSecretaryPendingGivenProcessedMessageRemovedThenStillQueuesCleanup(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryClaim(t, f.store)
		_, err := f.store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
		require.NoError(t, err)
		if f.memory != nil {
			delete(f.memory.messages, "m1")
			require.NoError(t, f.memory.BackfillSecretaryJobs(t.Context()))
		} else {
			_, err = f.postgres.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, 1, status.PendingCount)
		assert.Equal(t, "waiting", status.ExecutionState)
		progress := secretaryProgress(t, f.store.(domain.SecretaryProgressStore))
		assert.Zero(
			t,
			progress.Counts.PendingMessages,
			"removed content needs cleanup without being current visible text",
		)
	})
}

func TestSecretaryPendingGivenReplacementArraysThenMatchesCanonicalFallbackPolicy(t *testing.T) {
	for _, test := range []struct {
		name, raw, parent string
		want              int
	}{
		{"valid_replacement", `{"replaces_message_ids":["m1",null]}`, "unrelated", 0},
		{"valid_array_wins_parent", `{"replaces_message_ids":["unrelated"]}`, "m1", 1},
		{"null_array_does_not_poison_lookup", `{"replaces_message_ids":[null]}`, "m1", 1},
		{"empty_array_uses_parent", `{"replaces_message_ids":[]}`, "m1", 0},
		{"invalid_array_uses_parent", `{"replaces_message_ids":["unrelated",123]}`, "m1", 0},
		{"invalid_array_does_not_hide_member", `{"replaces_message_ids":["m1",123]}`, "unrelated", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				if f.memory != nil {
					f.memory.messages["display"] = Message{
						MessageID: "display", AgentID: "source", SessionID: "session", Role: "assistant",
						MessageType: domain.MessageTypePaxArtifact, ParentMessageID: test.parent,
						RawJSON: []byte(test.raw),
					}
				} else {
					_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json,session_seq)
 VALUES('display','source','session','test','inbound','assistant','pax:artifact',$1,$2::jsonb,2)`, test.parent, test.raw)
					require.NoError(t, err)
				}
				status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
				require.NoError(t, err)
				assert.Equal(t, test.want, status.PendingCount)
			})
		})
	}
}

func TestSecretaryPendingGivenCrossSessionInvalidationThenCountsOnlyActualResetWork(t *testing.T) {
	for _, nested := range []bool{false, true} {
		secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
			job := secretaryClaim(t, f.store)
			baseline, err := f.store.PublishSecretaryJob(
				t.Context(),
				job,
				secretaryPublication(job),
			)
			require.NoError(t, err)
			f.addTaskSource(t)
			other := secretaryClaim(t, f.store)
			child, err := f.store.PublishSecretaryJob(
				t.Context(),
				other,
				secretaryPublication(other),
			)
			require.NoError(t, err)
			request := domain.PublishSummaryRequest{
				SummaryID: baseline.SummaryID, ExpectedRevision: baseline.Revision,
				RequestID: "cross-session-reference", SubjectType: baseline.SubjectType,
				SubjectID: baseline.SubjectID, Title: "Related source", Body: "Related evidence",
				Coverage: append([]domain.MessageVersionRef(nil), child.Coverage...),
			}
			if nested {
				request.Coverage = nil
				request.SummarySources = []domain.SummaryRef{
					{SummaryID: child.SummaryID, Revision: child.Revision},
				}
			}
			_, err = f.store.(domain.SummaryStore).PublishSummary(t.Context(), "owner", request)
			require.NoError(t, err)
			if f.memory != nil {
				part := f.memory.messageParts[messagePartKey{MessageID: "m_other_source", Index: 0}]
				part.Text = "changed"
				f.memory.messageParts[messagePartKey{MessageID: "m_other_source", Index: 0}] = part
				session := f.memory.sessions[sessionKey("source", "session_two")]
				session.ArchivedAt = f.now
				f.memory.sessions[sessionKey("source", "session_two")] = session
				require.NoError(t, f.memory.BackfillSecretaryJobs(t.Context()))
			} else {
				_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE message_parts SET text='changed' WHERE message_id='m_other_source'; UPDATE agent_sessions SET archived_at=NOW() WHERE session_id='session_two'`)
				require.NoError(t, err)
			}
			status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
			require.NoError(t, err)
			assert.Equal(
				t,
				1,
				status.PendingCount,
				"the visible origin needs a reset, even with no own unread messages",
			)
			if f.memory != nil {
				part := f.memory.messageParts[messagePartKey{MessageID: "m_other_source", Index: 0}]
				part.Text = "Another source"
				f.memory.messageParts[messagePartKey{MessageID: "m_other_source", Index: 0}] = part
				session := f.memory.sessions[sessionKey("source", "session_two")]
				session.ArchivedAt = nil
				f.memory.sessions[sessionKey("source", "session_two")] = session
				require.NoError(t, f.memory.BackfillSecretaryJobs(t.Context()))
			} else {
				_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE message_parts SET text='Another source' WHERE message_id='m_other_source'; UPDATE agent_sessions SET archived_at=NULL WHERE session_id='session_two'`)
				require.NoError(t, err)
			}
			status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
			require.NoError(t, err)
			assert.Zero(
				t,
				status.PendingCount,
				"a dirty invalidation whose exact source is restored is a no-op",
			)
		})
	}
}

func TestSecretaryPendingGivenReplacementInAnotherSessionThenKeepsBothUnreadSessions(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		f.addTaskSource(t)
		if f.memory != nil {
			f.memory.messages["foreign_display"] = Message{
				MessageID: "foreign_display", AgentID: "source", SessionID: "session_two",
				Role: "assistant", MessageType: domain.MessageTypePaxArtifact,
				ParentMessageID: "m1", RawJSON: []byte(`{"replaces_message_ids":["m1"]}`),
			}
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json,session_seq)
 VALUES('foreign_display','source','session_two','test','inbound','assistant','pax:artifact','m1',
 '{"replaces_message_ids":["m1"]}'::jsonb,2)`)
			require.NoError(t, err)
		}
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(
			t,
			2,
			status.PendingCount,
			"a replacement applies only inside its agent and session",
		)
	})
}

func TestSecretaryPendingGivenUnknownExecutionAndRevertedBodyThenKeepsRecoveryVisible(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := secretaryClaim(t, f.store)
		_, err := f.store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
		require.NoError(t, err)
		changeBody := func(body string) {
			if f.memory != nil {
				part := f.memory.messageParts[messagePartKey{MessageID: "m1", Index: 0}]
				part.Text = body
				f.memory.messageParts[messagePartKey{MessageID: "m1", Index: 0}] = part
				require.NoError(t, f.memory.BackfillSecretaryJobs(t.Context()))
			} else {
				_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE message_parts SET text=$1 WHERE message_id='m1'`, body)
				require.NoError(t, err)
			}
		}
		changeBody("changed")
		job = f.begin(t, f.prepare(t))
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
		changeBody("original")
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, 1, status.PendingCount)
		assert.Equal(t, "unknown", status.ExecutionState)
		assert.False(t, status.Processing)
	})
}

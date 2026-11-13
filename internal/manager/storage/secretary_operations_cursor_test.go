package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryOperationToolRow(t *testing.T, f *secretaryLifecycleFixture, id string) {
	t.Helper()
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, id, "Internal tool result", 2)
		message := f.memory.messages[id]
		message.Role, message.MessageType = "tool", "tool_result"
		f.memory.messages[id] = message
		f.memory.refreshSecretaryMessagesLocked(f.memory.secretaryState.jobs["1"])
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,session_seq,status)
 VALUES($1,'source','session','test','inbound','tool','tool_result',2,'complete')`, id)
	require.NoError(t, err)
	_, err = f.postgres.db.ExecContext(
		t.Context(),
		`INSERT INTO message_parts(message_id,part_index,part_type,text)
 VALUES($1,0,'text','Internal tool result')`,
		id,
	)
	require.NoError(t, err)
}

func TestSecretaryOperationsGivenUncoveredToolRowsThenManualCompletionNeedsNoAgentForCursorCleanup(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryOperationToolRow(t, f, "tool")
		_, err := f.store.UpdateSecretarySettings(
			t.Context(),
			"owner",
			domain.SecretarySettingsUpdate{
				AgentID: "secretary", Enabled: false, ExpectedRevision: f.settings.Revision},
		)
		require.NoError(t, err)
		operation := secretaryOperationDraft(t, f, "human_summary", "pending")
		require.Len(t, operation.Targets, 1)
		_, err = secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
		require.NoError(t, err)
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Zero(t, status.PendingCount)
		tasks, err := secretaryTasksStore(
			t,
			f,
		).ListSecretaryTasks(t.Context(), "owner", domain.SecretaryTaskQuery{})
		require.NoError(t, err)
		assert.Empty(t, tasks.Items)
		if f.memory != nil {
			job := f.memory.secretaryState.jobs["1"]
			assert.Equal(t, job.desired, job.processed)
		} else {
			var complete bool
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT desired_revision=processed_revision
 FROM secretary_jobs WHERE source_id=1`).Scan(&complete))
			assert.True(t, complete)
		}
	})
}

func TestSecretaryOperationsGivenPreviouslyCoveredRemovalThenManualOtherRangeKeepsRemovalPending(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryOperationInitialSummary(t, f)
		if f.memory != nil {
			delete(f.memory.messages, "m1")
			f.memory.refreshSecretaryMessagesLocked(f.memory.secretaryState.jobs["1"])
		} else {
			_, err := f.postgres.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
			require.NoError(t, err)
		}
		secretaryOperationMessage(t, f, "m2", "A separate completed item", 2)
		operation := secretaryOperationDraft(t, f, "human_summary", "selected", "m2")
		_, err := secretaryOperationStore(
			f,
		).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
		require.NoError(t, err)
		if f.memory != nil {
			job := f.memory.secretaryState.jobs["1"]
			assert.Less(t, job.processed, job.desired)
		} else {
			var pending bool
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT desired_revision>processed_revision
 FROM secretary_jobs WHERE source_id=1`).Scan(&pending))
			assert.True(t, pending)
		}
		work := secretaryClaim(t, f.store)
		assert.Contains(t, work.RemovedMessageIDs, "m1")
		assert.True(t, work.ResetRequired)
	})
}

func TestSecretaryOperationsGivenUncoveredNonTextRowsThenSkipsOnlySettledInvisibleChanges(
	t *testing.T,
) {
	for _, tc := range []struct {
		name, status, partType, text string
		pending                      bool
	}{
		{name: "settled_whitespace", status: "complete", partType: "text", text: " \n\t"},
		{name: "settled_tool_parts", status: "complete", partType: "tool_result", text: "Internal result"},
		{name: "incomplete_empty_message", status: "streaming", partType: "text", pending: true},
		{name: "visible_pending_message", status: "complete", partType: "text", text: "Still needs summary", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				if f.memory != nil {
					secretaryMemoryMessage(f.memory, "m2", tc.text, 2)
					message := f.memory.messages["m2"]
					message.Status = tc.status
					f.memory.messages["m2"] = message
					key := messagePartKey{MessageID: "m2", Index: 0}
					part := f.memory.messageParts[key]
					part.PartType = tc.partType
					f.memory.messageParts[key] = part
					f.memory.refreshSecretaryMessagesLocked(f.memory.secretaryState.jobs["1"])
				} else {
					_, err := f.postgres.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,session_seq,status)
 VALUES('m2','source','session','test','inbound','assistant','chat',2,$1)`, tc.status)
					require.NoError(t, err)
					_, err = f.postgres.db.ExecContext(t.Context(), `INSERT INTO message_parts(message_id,part_index,part_type,text)
 VALUES('m2',0,$1,$2)`, tc.partType, tc.text)
					require.NoError(t, err)
				}
				operation := secretaryOperationDraft(t, f, "human_summary", "selected", "m1")
				_, err := secretaryOperationStore(
					f,
				).SubmitSecretaryOperation(t.Context(), "owner", operation.OperationID, secretaryHumanInput(operation))
				require.NoError(t, err)
				if f.memory != nil {
					job := f.memory.secretaryState.jobs["1"]
					assert.Equal(t, tc.pending, job.desired > job.processed)
				} else {
					var pending bool
					require.NoError(t, f.postgres.db.QueryRowContext(t.Context(), `SELECT desired_revision>processed_revision
 FROM secretary_jobs WHERE source_id=1`).Scan(&pending))
					assert.Equal(t, tc.pending, pending)
				}
			})
		})
	}
}

package storage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryInputLimitGivenWrappedFailureThenBlocksOnlyDeterministicInputLimits(
	t *testing.T,
) {
	failure := secretaryInputLimit("full-source byte limit")
	assert.ErrorIs(t, failure, ErrConflict)
	assert.True(t, secretaryInputBlocked(failure))
	assert.True(t, secretaryInputBlocked(fmt.Errorf("load source: %w", failure)))
	assert.False(t, secretaryInputBlocked(ErrConflict))
	assert.False(t, secretaryInputBlocked(assert.AnError))
	assert.False(t, secretaryInputBlocked(nil))
	for _, state := range []string{"", "pending", "committed", "blocked"} {
		assert.True(t, secretaryInputFailureVisible(failure.Error(), state, true))
	}
	for _, state := range []string{"running", "unknown", "output_received"} {
		assert.False(t, secretaryInputFailureVisible(failure.Error(), state, true))
	}
	assert.False(t, secretaryInputFailureVisible(failure.Error(), "", false))
	assert.False(t, secretaryInputFailureVisible("temporary failure", "", true))
}

func secretaryBudgetSource(t *testing.T, f *secretaryLifecycleFixture, body string, reply bool) {
	t.Helper()
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, "m1", body, 1)
		message := f.memory.messages["m1"]
		message.Role, message.MessageType = "user", "user_message"
		f.memory.messages["m1"] = message
		if reply {
			secretaryMemoryMessage(f.memory, "m2", "Earlier reasoning.\n\nReply conclusion.", 2)
		}
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(),
		`UPDATE messages SET role='user',message_type='user_message' WHERE message_id='m1'`)
	require.NoError(t, err)
	_, err = f.postgres.db.ExecContext(t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1' AND part_type='text'`, body)
	require.NoError(t, err)
	if reply {
		_, err = f.postgres.db.ExecContext(t.Context(), `
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 VALUES ('m2','source','session','test','inbound','assistant','agent_message_chunk',2);
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 VALUES ('m2',0,'text',E'Earlier reasoning.\n\nReply conclusion.')`)
		require.NoError(t, err)
	}
}

func TestSecretaryBudgetGivenLongUserMessageThenIncludesItsReplyAndPreservesFullSource(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		body := "Request heading\n\n" + strings.Repeat("x", 314*1024) + "\n\nRequest ending"
		secretaryBudgetSource(t, f, body, true)
		work := secretaryClaim(t, f.store)
		require.Len(t, work.Messages, 2)
		assert.NoError(t, secretaryWithinBounds(work.Messages))
		assert.Equal(t, body, work.Messages[0].Body)
		assert.Equal(t, domain.SearchContentHash(body), work.Messages[0].ContentHash)
		assert.Equal(t, "m2", work.Messages[1].SourceID)
		assert.Equal(t, "Earlier reasoning.\n\nReply conclusion.", work.Messages[1].Body)
		assert.Equal(t, domain.SecretaryInputPolicyVersion, work.InputPolicyVersion)
		work = secretaryNodesReadyWork(t, f.store, work)
		publication, err := secretary.BuildNodePublication(work, `{
 "summaries":[{"action":"create","proposal_key":"request","title":"Request",
 "body":"The request and its reply.","source_message_ids":["m1","m2"],
 "claims":[{"text":"The request has an opening and closing question.","evidence":[
 {"message_id":"m1","quote":"Request heading"},
 {"message_id":"m1","quote":"Request ending"}]}]}],
 "outcomes":[{"message_id":"m1","action":"summarized","summary_keys":["request"]},
 {"message_id":"m2","action":"summarized","summary_keys":["request"]}]
 }`, domain.SecretaryToolReads{})
		require.NoError(t, err)
		versions, err := f.store.PublishSecretaryBatch(t.Context(), work, publication)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		version, err := f.store.(domain.SummaryStore).GetSummary(
			t.Context(),
			"owner",
			versions[0].SummaryID,
			0,
		)
		require.NoError(t, err)
		require.Len(t, version.Claims, 1)
		require.Len(t, version.Claims[0].Evidence, 2)
		assert.Equal(t, "head_tail", version.Claims[0].Evidence[0].View)
		assert.Equal(t, "head_tail", version.Claims[0].Evidence[1].View)
		var userRef domain.MessageVersionRef
		for _, ref := range version.Coverage {
			if ref.MessageID == "m1" {
				userRef = ref
			}
		}
		frozen, err := f.store.(domain.SummaryStore).ReadMessageVersion(
			t.Context(),
			"owner",
			userRef,
		)
		require.NoError(t, err)
		assert.Equal(t, body, frozen.Body)
		assert.Equal(t, domain.SearchContentHash(body), frozen.ContentHash)
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Zero(t, status.PendingCount)
	})
}

func TestSecretaryBudgetGivenUnsupportedFullSourceThenBlocksUntilSourceChanges(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		secretaryBudgetSource(t, f, strings.Repeat("x", domain.SecretaryMaxSourceBytes+1), false)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		var firstAttempts int
		if f.memory != nil {
			job := f.memory.secretaryState.jobs["1"]
			assert.True(t, job.executionBlocked)
			assert.Zero(t, job.processed)
			assert.Empty(t, job.work.LeaseToken)
			assert.Contains(t, job.lastError, "input size requires source change or explicit retry")
			*f.now = f.now.Add(time.Minute)
		} else {
			var blocked bool
			var processed int64
			var lease, failure string
			err = f.postgres.db.QueryRowContext(t.Context(),
				`SELECT execution_blocked,processed_revision,lease_token,last_error,attempts
 FROM secretary_jobs WHERE source_id=1`).Scan(&blocked, &processed, &lease, &failure, &firstAttempts)
			require.NoError(t, err)
			assert.True(t, blocked)
			assert.Zero(t, processed)
			assert.Empty(t, lease)
			assert.Equal(t, 1, firstAttempts)
			assert.Contains(t, failure, "input size requires source change or explicit retry")
			_, err = f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_jobs SET available_at=NOW()`)
			require.NoError(t, err)
		}
		jobs, err = f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		if f.postgres != nil {
			var attempts int
			require.NoError(t, f.postgres.db.QueryRowContext(t.Context(),
				`SELECT attempts FROM secretary_jobs WHERE source_id=1`).Scan(&attempts))
			assert.Equal(t, firstAttempts, attempts)
		}
		secretaryBudgetSource(t, f, "fixed source", false)
		work := secretaryClaim(t, f.store)
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "fixed source", work.Messages[0].Body)
	})
}

func TestSecretaryBudgetGivenPreviousCommittedBatchThenShowsCurrentInputFailure(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		work := secretaryNodesReadyJob(t, f.store)
		_, err := f.store.PublishSecretaryBatch(
			t.Context(),
			work,
			secretaryNodesPublication(work, "initial"),
		)
		require.NoError(t, err)
		secretaryBudgetSource(t, f, strings.Repeat("x", domain.SecretaryMaxSourceBytes+1), false)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		detail, err := f.store.(domain.SecretaryTaskStore).GetSecretaryTask(
			t.Context(),
			"owner",
			"1",
			domain.SecretaryTaskQuery{},
		)
		require.NoError(t, err)
		assert.Equal(t, "blocked", detail.Task.ExecutionState)
		assert.Equal(t, "input_size_limit", detail.Task.ErrorCode)
		assert.Contains(t, detail.Task.ErrorMessage, "exceeds the supported input size")
		assert.Nil(t, detail.Task.NextRetryAt)
	})
}

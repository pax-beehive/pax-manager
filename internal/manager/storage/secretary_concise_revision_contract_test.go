package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryConciseRevisionGivenRoutineHistoryThenKeepsDecisionsAndProcessesOmissionsWithoutDeletingSources(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		const decision = "Use PostgreSQL as the source of truth."
		const routine = "Ran curl three times; all requests returned 200."
		const constraint = "Search must remain restricted to the current owner."
		const acknowledgment = "OK, thanks."
		putConciseRevisionSource(t, f, "m1", decision, 1)
		putConciseRevisionSource(t, f, "m2", routine, 2)
		firstJob := secretaryNodesReadyJob(t, f.store)
		firstPublication, err := secretary.BuildNodePublication(firstJob, `{
  "summaries":[{"action":"create","proposal_key":"search","title":"Search design",
    "body":"Use PostgreSQL as the source of truth. Ran curl three times; all requests returned 200.",
    "source_message_ids":["m1","m2"],"claims":[
      {"text":"Use PostgreSQL as the source of truth.","evidence":[{"message_id":"m1","quote":"Use PostgreSQL as the source of truth."}]},
      {"text":"Ran curl three times; all requests returned 200.","evidence":[{"message_id":"m2","quote":"Ran curl three times; all requests returned 200."}]}]}],
  "outcomes":[{"message_id":"m1","action":"summarized","summary_keys":["search"]},
    {"message_id":"m2","action":"summarized","summary_keys":["search"]}]
}`, domain.SecretaryToolReads{})
		require.NoError(t, err)
		first, err := f.store.PublishSecretaryBatch(t.Context(), firstJob, firstPublication)
		require.NoError(t, err)
		require.Len(t, first, 1)
		original := first[0]
		require.Len(t, original.Claims, 2)
		require.Len(t, original.Coverage, 2)

		putConciseRevisionSource(t, f, "m3", constraint, 3)
		putConciseRevisionSource(t, f, "m4", acknowledgment, 4)
		next := secretaryNodesReadyJob(t, f.store)
		require.Len(
			t,
			next.Messages,
			2,
			"already processed messages must only come from the baseline",
		)
		require.Len(t, next.CurrentSummaries, 1)
		raw := fmt.Sprintf(`{
  "summaries":[{"action":"revise","summary_id":%q,"expected_revision":%d,
    "title":"Search design","body":"PostgreSQL is authoritative; search is owner-scoped.",
    "source_message_ids":["m1","m3"],"claims":[
      {"id":"claim_1","text":"Use PostgreSQL as the source of truth.","evidence":[{"message_id":"m1","quote":"Use PostgreSQL as the source of truth."}]},
      {"text":"Search must remain restricted to the current owner.","evidence":[{"message_id":"m3","quote":"Search must remain restricted to the current owner."}]}]}],
  "outcomes":[{"message_id":"m3","action":"summarized","summary_keys":[%q]},
    {"message_id":"m4","action":"no_summary","reason":"Acknowledgment adds no durable information."}]
}`, original.SummaryID, original.Revision, original.SummaryID)
		publication, err := secretary.BuildNodePublication(next, raw, domain.SecretaryToolReads{})
		require.NoError(t, err, "revision may omit routine claims and their coverage")
		versions, err := f.store.PublishSecretaryBatch(t.Context(), next, publication)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		latest := versions[0]
		assert.Equal(t, original.SummaryID, latest.SummaryID)
		assert.Equal(t, original.Revision+1, latest.Revision)
		assert.Equal(t, "PostgreSQL is authoritative; search is owner-scoped.", latest.Body)
		require.Len(t, latest.Claims, 2)
		assert.Equal(
			t,
			original.Claims[0],
			latest.Claims[0],
			"retain the important baseline decision and exact proof",
		)
		assert.Equal(t, constraint, latest.Claims[1].Text)
		require.Len(t, latest.Coverage, 2)
		assert.Equal(t, original.Coverage[0], latest.Coverage[0])
		assert.Equal(t, "m3", latest.Coverage[1].MessageID)

		reader := f.store.(domain.SummaryStore)
		current, err := reader.GetSummary(t.Context(), "owner", original.SummaryID, 0)
		require.NoError(t, err)
		assert.Equal(t, latest.Revision, current.Revision)
		assert.Equal(t, latest.Body, current.Body)
		assert.Equal(t, latest.Claims, current.Claims)
		assert.Equal(t, latest.Coverage, current.Coverage)
		historical, err := reader.GetSummary(
			t.Context(),
			"owner",
			original.SummaryID,
			original.Revision,
		)
		require.NoError(t, err)
		assert.Equal(t, original.Body, historical.Body)
		assert.Equal(t, original.Claims, historical.Claims)
		assert.Equal(t, original.Coverage, historical.Coverage)
		frozenRoutine, err := reader.ReadMessageVersion(t.Context(), "owner", original.Coverage[1])
		require.NoError(t, err)
		assert.Equal(t, routine, frozenRoutine.Body)
		for id, text := range map[string]string{"m1": decision, "m2": routine, "m3": constraint, "m4": acknowledgment} {
			doc, readErr := f.store.(domain.SearchSourceStore).ReadSearchDocument(
				t.Context(),
				"owner",
				"message",
				id,
			)
			require.NoError(t, readErr)
			assert.Equal(
				t,
				text,
				doc.Body,
				"summary filtering must not delete source message %s",
				id,
			)
		}
		require.NoError(t, f.store.BackfillSecretaryJobs(t.Context()))
		progress := secretaryProgress(t, f.store.(domain.SecretaryProgressStore))
		assert.Equal(
			t,
			4,
			progress.Counts.CompletedMessages,
			"no_summary and dropped old coverage retain processing progress",
		)
		assert.Zero(t, progress.Counts.PendingMessages)
		jobs, err := f.store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
		require.NoError(t, err)
		assert.Empty(t, jobs, "omitted content must not be requeued by backfill")
	})
}

func putConciseRevisionSource(
	t *testing.T,
	f *secretaryLifecycleFixture,
	id, body string,
	seq int64,
) {
	t.Helper()
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, id, body, seq)
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(), `
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq,status)
VALUES($1,'source','session','test','inbound','assistant','agent_message_chunk',$2,'complete')
ON CONFLICT(message_id) DO NOTHING`, id, seq)
	require.NoError(t, err)
	_, err = f.postgres.db.ExecContext(t.Context(), `
INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES($1,0,'text',$2)
ON CONFLICT(message_id,part_index) DO UPDATE SET text=EXCLUDED.text`, id, body)
	require.NoError(t, err)
}

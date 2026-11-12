package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func secretaryRecurringMessage(t *testing.T, f *secretaryLifecycleFixture, seq int) {
	t.Helper()
	id, body := fmt.Sprintf("m%d", seq), "Completed another manual deployment."
	if f.memory != nil {
		secretaryMemoryMessage(f.memory, id, body, int64(seq))
		return
	}
	_, err := f.postgres.db.ExecContext(t.Context(), `
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 VALUES ($1,'source','session','test','inbound','assistant','agent_message_chunk',$2)
 ON CONFLICT(message_id) DO NOTHING`, id, seq)
	require.NoError(t, err)
	if seq == 1 {
		_, err = f.postgres.db.ExecContext(t.Context(),
			`UPDATE message_parts SET text=$1 WHERE message_id=$2 AND part_type='text'`, body, id)
	} else {
		_, err = f.postgres.db.ExecContext(t.Context(),
			`INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ($1,0,'text',$2)`, id, body)
	}
	require.NoError(t, err)
}

func TestSecretaryRecurringActivityGivenRepeatedDeploymentsThenKeepsOneSummaryAndCompletesDelta(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		for seq := 1; seq <= 4; seq++ {
			secretaryRecurringMessage(t, f, seq)
		}
		work := secretaryNodesReadyJob(t, f.store)
		require.Len(t, work.Messages, 4)
		publication, err := secretary.BuildNodePublication(work, `{
 "summaries":[{"action":"create","proposal_key":"manual-deployments",
 "title":"Manual deployment history","body":"Repeated manual deployments were performed.",
 "source_message_ids":["m1","m2"]}],
 "outcomes":[
 {"message_id":"m1","action":"summarized","summary_keys":["manual-deployments"]},
 {"message_id":"m2","action":"summarized","summary_keys":["manual-deployments"]},
 {"message_id":"m3","action":"no_summary","reason":"Same routine deployment already represented."},
 {"message_id":"m4","action":"no_summary","reason":"Same routine deployment already represented."}]}
 `, domain.SecretaryToolReads{})
		require.NoError(t, err)
		versions, err := f.store.PublishSecretaryBatch(t.Context(), work, publication)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		summaries := f.store.(domain.SummaryStore)
		initial, err := summaries.GetSummary(t.Context(), "owner", versions[0].SummaryID, 0)
		require.NoError(t, err)
		assert.Len(t, initial.Coverage, 2, "repeated events do not require cumulative coverage")
		assert.Empty(t, initial.Claims, "a routine activity does not need one claim per event")

		secretaryRecurringMessage(t, f, 5)
		work = secretaryNodesReadyJob(t, f.store)
		require.Len(t, work.Messages, 1)
		assert.Equal(t, "m5", work.Messages[0].SourceID)
		publication, err = secretary.BuildNodePublication(work, `{
 "summaries":[],"outcomes":[{"message_id":"m5","action":"no_summary",
 "reason":"Same routine deployment already represented."}]}`, domain.SecretaryToolReads{})
		require.NoError(t, err)
		versions, err = f.store.PublishSecretaryBatch(t.Context(), work, publication)
		require.NoError(t, err)
		assert.Empty(t, versions)
		current, err := summaries.GetSummary(t.Context(), "owner", initial.SummaryID, 0)
		require.NoError(t, err)
		assert.Equal(t, initial, current, "another routine event must not create a revision")
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Zero(t, status.PendingCount, "no_summary still completes the new message")
	})
}

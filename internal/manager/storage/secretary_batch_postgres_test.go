package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryPostgresGivenInterleavedEventsThenFillsAndPublishesCanonicalBatches(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 SELECT 'event_'||i,'source','session','test','inbound','assistant',
 CASE WHEN i%8=0 THEN 'agent_message_chunk' ELSE 'agent_thought_chunk' END,i+1
 FROM generate_series(1,512) i;
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 SELECT 'event_'||i,0,'text',CASE WHEN i%8=0 THEN 'visible original' ELSE 'private reasoning' END
 FROM generate_series(1,512) i;`)
	require.NoError(t, err)
	secretaryEnable(t, store)
	first := secretaryClaim(t, store)
	require.Len(t, first.Messages, domain.SecretaryMaxMessages)
	assert.EqualValues(t, 505, first.ThroughRevision)
	for _, doc := range first.Messages {
		assert.NotContains(t, doc.Body, "private reasoning")
	}
	_, err = store.PublishSecretaryJob(t.Context(), first, secretaryPublication(first))
	require.NoError(t, err)
	next := secretaryClaim(t, store)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "event_512", next.Messages[0].SourceID)
	assert.EqualValues(t, 513, next.ThroughRevision)
	published, err := store.PublishSecretaryJob(t.Context(), next, secretaryPublication(next))
	require.NoError(t, err)
	assert.Len(t, published.Coverage, 65)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
}

func TestSecretaryPostgresGivenInvisibleScanBudgetThenNextClaimReachesVisibleSource(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	first := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), first, secretaryPublication(first))
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 SELECT 'event_'||i,'source','session','test','inbound','assistant',
 CASE WHEN i=1101 THEN 'agent_message_chunk' ELSE 'agent_thought_chunk' END,i+1
 FROM generate_series(1,1101) i;
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 SELECT 'event_'||i,0,'text','body' FROM generate_series(1,1101) i;`)
	require.NoError(t, err)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	var remaining int
	require.NoError(t, store.db.QueryRowContext(t.Context(), `
 SELECT count(*) FROM secretary_message_state m JOIN secretary_jobs j USING(source_id)
 WHERE m.change_revision>j.processed_revision`).Scan(&remaining))
	assert.Equal(t, 77, remaining)
	next := secretaryClaim(t, store)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "event_1101", next.Messages[0].SourceID)
}

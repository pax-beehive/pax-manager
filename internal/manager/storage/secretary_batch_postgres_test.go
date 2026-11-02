package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryPostgresGivenCompactAssistantViewThenFreezesItsCompleteVersion(t *testing.T) {
	store := secretaryPostgresFixture(t)
	body := strings.Repeat("earlier detail ", 12*1024) + "\n\nFinal decision."
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE messages SET role='assistant' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1' AND part_type='text'`,
		body,
	)
	require.NoError(t, err)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, body, work.Messages[0].Body)
	assert.Greater(t, len(body), domain.SecretaryMaxBytes)
	assert.NoError(t, secretaryWithinBounds(work.Messages))
	version, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	require.Len(t, version.Coverage, 1)
	frozen, err := store.ReadMessageVersion(t.Context(), "owner", version.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, body, frozen.Body)
	assert.Equal(t, domain.SearchContentHash(body), frozen.ContentHash)
}

func TestSecretaryPostgresGivenOversizedSerializedSourceThenDoesNotTransferItsAggregate(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=repeat('x',$1) WHERE message_id='m1'`,
		domain.SecretaryMaxSourceBytes+1,
	)
	require.NoError(t, err)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = loadSearchMessageWithByteLimit(t.Context(), tx, "m1", domain.SecretaryMaxSourceBytes)
	assert.ErrorContains(t, err, "full-source byte limit")
}

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

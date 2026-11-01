package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryRollingPostgresGivenLegacyFragmentsThenRebuildsIntoStableHead(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	legacy := secretaryPublication(work)
	legacy.SummaryID = "legacy-fragment"
	_, err := store.PublishSummary(t.Context(), "owner", legacy)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET processed_revision=desired_revision,lease_token='',lease_until=NULL`,
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_message_state SET processed_hash=$1,processed_canonicalization_version=$2,processed_owner_user_id='owner'`,
		domain.SearchContentHash("original"),
		domain.SearchCanonicalizationVersion,
	)
	require.NoError(t, err)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	rebuild := secretaryClaim(t, store)
	assert.True(t, rebuild.ResetRequired)
	assert.Nil(t, rebuild.CurrentSummary)
	assert.Equal(t, "rolling_summary_rebuild", rebuild.ResetReason)
	require.Len(t, rebuild.Messages, 1)
	assert.Equal(t, domain.SecretarySummaryID("owner", rebuild.SourceID), rebuild.TargetSummaryID)
	_, err = store.PublishSecretaryJob(t.Context(), rebuild, secretaryPublication(rebuild))
	require.NoError(t, err)
	old, err := store.GetSummary(t.Context(), "owner", "legacy-fragment", 1)
	require.NoError(t, err)
	assert.Equal(t, legacy.Body, old.Body)
}

func TestSecretaryRollingPostgresGivenDeletionThenRetriesResetAndClearsAllRemovedSources(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
	require.NoError(t, err)
	reset := secretaryClaim(t, store)
	assert.True(t, reset.ResetRequired)
	assert.Nil(t, reset.CurrentSummary)
	assert.Empty(t, reset.Messages)
	require.NoError(t, store.FailSecretaryJob(t.Context(), reset, domain.ErrConflict))
	_, err = store.db.ExecContext(t.Context(), `UPDATE secretary_jobs SET available_at=NOW()`)
	require.NoError(t, err)
	retry := secretaryClaim(t, store)
	assert.Equal(t, reset.RunID, retry.RunID)
	request := secretaryPublication(retry)
	request.Body = "This session has no remaining readable messages."
	current, err := store.PublishSecretaryJob(t.Context(), retry, request)
	require.NoError(t, err)
	assert.EqualValues(t, 2, current.Revision)
	assert.Empty(t, current.Coverage)
	assert.Empty(t, current.SummarySources)
	read, err := store.GetSummary(t.Context(), "owner", current.SummaryID, 0)
	require.NoError(t, err)
	assert.Equal(t, request.Body, read.Body)
	_, err = store.GetSummary(t.Context(), "owner", current.SummaryID, 1)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestSecretaryRollingPostgresGivenRelatedSourceThenLinksItWithoutAdvancingItsCheckpoint(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('secretary','related');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq) VALUES('related','secretary','related','test','inbound','user','chat',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES('related',0,'text','Related fact');`,
	)
	require.NoError(t, err)
	ref := summaryTestRef("related", "Related fact")
	request := secretaryPublication(work)
	request.Coverage = append(request.Coverage, ref)
	_, err = store.PublishSecretaryJob(t.Context(), work, request)
	assert.ErrorIs(t, err, ErrConflict)
	work.AllowedRelatedMessages = []domain.MessageVersionRef{ref}
	version, err := store.PublishSecretaryJob(t.Context(), work, request)
	require.NoError(t, err)
	assert.Len(t, version.Coverage, 2)
	var completed int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE message_id='related' AND processed_hash<>''`).
			Scan(&completed),
	)
	assert.Zero(t, completed)
	_, err = store.GetSummary(t.Context(), "other", version.SummaryID, version.Revision)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSecretaryRollingPostgresGivenMessageMovesThenQueuesRemovalAndNewSourceIndependently(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('secretary','destination');
 UPDATE messages SET agent_id='secretary',session_id='destination' WHERE message_id='m1';`,
	)
	require.NoError(t, err)
	removal := secretaryClaim(t, store)
	assert.Equal(t, work.SourceID, removal.SourceID)
	assert.Empty(t, removal.Messages)
	assert.True(t, removal.ResetRequired)
	assert.Nil(t, removal.CurrentSummary)
	assert.Contains(t, removal.RemovedMessageIDs, "m1")
	current, err := store.PublishSecretaryJob(t.Context(), removal, secretaryPublication(removal))
	require.NoError(t, err)
	assert.Empty(t, current.Coverage)
	destination := secretaryClaim(t, store)
	assert.Equal(t, "destination", destination.SourceSessionID)
	require.Len(t, destination.Messages, 1)
	assert.Equal(t, "m1", destination.Messages[0].SourceID)
}

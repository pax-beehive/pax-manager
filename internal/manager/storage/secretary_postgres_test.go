package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryPostgresFixture(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("PAX_MANAGER_SECRETARY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_SECRETARY_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("secretary_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	for _, name := range []string{"init.sql", "summaries.sql", "search.sql", "secretary.sql"} {
		raw, readErr := os.ReadFile(filepath.Join("..", "..", "..", "db", name))
		require.NoError(t, readErr)
		_, err = db.ExecContext(t.Context(), string(raw))
		require.NoError(t, err)
	}
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO users(user_id,email) VALUES ('owner','owner@example.test'),('other','other@example.test');
 INSERT INTO agents(agent_id,owner_user_id,hostname,os,api_key_hash) VALUES ('source','owner','host','linux','source-key'),('secretary','owner','host','linux','secretary-key'),('other','other','host','linux','other-key');
 INSERT INTO agent_sessions(agent_id,session_id,session_name) VALUES ('source','session','Original session');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq) VALUES ('m1','source','session','test','inbound','assistant','agent_message_chunk',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m1',0,'text','original');`,
	)
	require.NoError(t, err)
	return NewPostgresStore(db, time.Now)
}

func TestSecretaryPostgresGivenPublicationThenCoverageCheckpointsAndVersionCommitTogether(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq) VALUES ('m2','source','session','test','inbound','user','chat',2); INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m2',0,'text','next message');`,
	)
	require.NoError(t, err)
	first, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	assert.EqualValues(t, 1, first.Revision)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
	assert.NotNil(t, status.LastCompletedAt)
	next := secretaryClaim(t, store)
	require.Len(t, next.Messages, 1)
	assert.Equal(t, "m2", next.Messages[0].SourceID)
	assert.Len(t, next.PreviousSummaries, 1)
	_, err = store.PublishSecretaryJob(t.Context(), next, secretaryPublication(next))
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='edited' WHERE message_id='m1';`,
	)
	require.NoError(t, err)
	updated := secretaryClaim(t, store)
	assert.Equal(t, first.SummaryID, updated.TargetSummaryID)
	assert.EqualValues(t, 1, updated.ExpectedRevision)
	second, err := store.PublishSecretaryJob(t.Context(), updated, secretaryPublication(updated))
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Revision)
	old, err := store.ReadMessageVersion(t.Context(), "owner", first.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, "original", old.Body)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='original' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	reverted := secretaryClaim(t, store)
	assert.NotEqual(t, work.RunID, reverted.RunID)
	assert.Equal(t, first.SummaryID, reverted.TargetSummaryID)
	third, publishErr := store.PublishSecretaryJob(
		t.Context(),
		reverted,
		secretaryPublication(reverted),
	)
	require.NoError(t, publishErr)
	assert.EqualValues(t, 3, third.Revision)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE messages SET updated_at=NOW() WHERE message_id='m1';`,
	)
	require.NoError(t, err)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1';`)
	require.NoError(t, err)
	_, err = store.GetSummary(t.Context(), "owner", first.SummaryID, 1)
	assert.ErrorIs(t, err, ErrNotFound)
	jobs, err = store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestSecretaryPostgresGivenDisableWhenInFlightThenPublicationIsFencedAtomically(t *testing.T) {
	store := secretaryPostgresFixture(t)
	cfg := secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", ExpectedRevision: cfg.Revision},
	)
	require.NoError(t, err)
	_, err = store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	active, err := store.SecretaryJobActive(t.Context(), work)
	require.NoError(t, err)
	assert.False(t, active)
	var versions, processed int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM summary_versions`).
			Scan(&versions),
	)
	assert.Zero(t, versions)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE processed_hash<>''`).
			Scan(&processed),
	)
	assert.Zero(t, processed)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed while disabled' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", Enabled: true, ExpectedRevision: 2},
	)
	require.NoError(t, err)
	reenabled := secretaryClaim(t, store)
	assert.Equal(t, "changed while disabled", reenabled.Messages[0].Body)
}

func TestSecretaryPostgresGivenRetryAndInternalSessionThenRunIdentityAndSchedulingAreSafe(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	require.NoError(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "secretary", "internal"),
	)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES ('secretary','internal'); INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq) VALUES ('internal','secretary','internal','test','inbound','assistant','agent_message_chunk',1); INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('internal',0,'text','Do not summarize');`,
	)
	require.NoError(t, err)
	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	require.NoError(
		t,
		store.RegisterSecretaryInternalSession(t.Context(), work, "secretary", "internal"),
	)
	var internalSource string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT source FROM agent_sessions WHERE agent_id='secretary' AND session_id='internal'`).
			Scan(&internalSource),
	)
	assert.Equal(t, domain.SecretaryInternalSource, internalSource)
	_, hiddenErr := store.ReadSearchDocument(t.Context(), "owner", "message", "internal")
	assert.ErrorIs(t, hiddenErr, ErrNotFound)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, fmt.Errorf("offline")))
	_, err = store.db.ExecContext(t.Context(), `UPDATE secretary_jobs SET available_at=NOW()`)
	require.NoError(t, err)
	retry := secretaryClaim(t, store)
	assert.Equal(t, work.RunID, retry.RunID)
	require.NoError(t, store.FailSecretaryJob(t.Context(), retry, fmt.Errorf("invalid output")))
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	fresh := secretaryClaim(t, store)
	assert.NotEqual(t, retry.RunID, fresh.RunID)
	assert.Equal(t, retry.TargetSummaryID, fresh.TargetSummaryID)
	var jobs int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_jobs`).Scan(&jobs),
	)
	assert.Equal(t, 1, jobs)
}

func TestSecretaryPostgresGivenOwnedSourceTransferThenCheckpointDoesNotSuppressNewOwner(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	cfg := secretaryEnable(t, store)
	old := secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), old, secretaryPublication(old))
	require.NoError(t, err)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"other",
		domain.SecretarySettingsUpdate{AgentID: "other", Enabled: true},
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`,
	)
	require.NoError(t, err)
	work := secretaryClaim(t, store)
	assert.Equal(t, "other", work.OwnerUserID)
	assert.NotEqual(t, old.RunID, work.RunID)
	require.Len(t, work.Messages, 1)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agents SET deleted_at=NOW() WHERE agent_id='secretary'`,
	)
	require.NoError(t, err)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", ExpectedRevision: cfg.Revision},
	)
	require.NoError(t, err)
}

func TestSecretaryPostgresGivenOversizeOrChangedSourceThenNoPrematureCheckpoint(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text=$1 WHERE message_id='m1'`,
		strings.Repeat("x", domain.SecretaryMaxBytes+1),
	)
	require.NoError(t, err)
	jobs, err := store.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs)
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Contains(t, status.LastError, "bounded batch")
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='fixed' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	work := secretaryClaim(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed during generation' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	_, err = store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	assert.ErrorIs(t, err, domain.ErrSourceVersionUnavailable)
	var processed int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_message_state WHERE processed_hash<>''`).
			Scan(&processed),
	)
	assert.Zero(t, processed)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, err))
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	fresh := secretaryClaim(t, store)
	request := secretaryPublication(fresh)
	request.Claims = []domain.SummaryClaim{
		{
			ID:       "invalid",
			Text:     "Invalid",
			Evidence: []domain.SummaryEvidence{{Message: request.Coverage[0], Quote: "never seen"}},
		},
	}
	_, err = store.PublishSecretaryJob(t.Context(), fresh, request)
	assert.ErrorIs(t, err, ErrConflict)
}

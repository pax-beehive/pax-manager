package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryProgressProjectionOracle(
	t *testing.T,
	store *PostgresStore,
	owner string,
) domain.SecretaryProgressPage {
	t.Helper()
	rows, err := store.db.QueryContext(
		t.Context(),
		secretaryProgressSQL,
		owner,
		secretaryProgressWhitespace,
	)
	require.NoError(t, err)
	progress, err := scanSecretaryProgress(rows, owner)
	require.NoError(t, err)
	return progress.page(owner, "", 100, time.Time{})
}

func requireSecretaryProgressProjectionMatchesSource(
	t *testing.T,
	store *PostgresStore,
	owner string,
) domain.SecretaryProgressPage {
	t.Helper()
	expected := secretaryProgressProjectionOracle(t, store, owner)
	actual, err := store.ReadSecretaryProgress(
		t.Context(),
		owner,
		domain.SecretaryProgressQuery{Limit: 100},
	)
	require.NoError(t, err)
	assert.False(t, actual.AsOf.IsZero())
	actual.AsOf = time.Time{}
	assert.Equal(t, expected, actual)
	return actual
}

func TestSecretaryProgressProjectionPostgresGivenDisabledSourceMutationsThenMatchesCurrentCoverage(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
UPDATE messages SET turn_id='turn-1' WHERE message_id='m1';
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq,turn_id)
VALUES ('m2','source','session','test','inbound','user','chat',2,'turn-1');
INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m2',0,'text','second');`)
	require.NoError(t, err)
	initial := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, 2, initial.Counts.PendingMessages)
	assert.Equal(t, 1, initial.Counts.PendingTurns)
	cfg := secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err = store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	completed := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, 2, completed.Counts.CompletedMessages)
	_, err = store.UpdateSecretarySettings(t.Context(), "owner", domain.SecretarySettingsUpdate{
		AgentID: "secretary", ExpectedRevision: cfg.Revision,
	})
	require.NoError(t, err)

	for _, mutation := range []struct {
		name, query string
	}{
		{"append text", `INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m1',1,'text',' late')`},
		{"repartition same body", `UPDATE message_parts SET text=CASE part_index WHEN 0 THEN 'orig' ELSE 'inal' END WHERE message_id='m1'`},
		{"remove turn grouping", `UPDATE messages SET turn_id=NULL WHERE message_id='m2'`},
		{"move source", `INSERT INTO agent_sessions(agent_id,session_id) VALUES ('source','second-session'); UPDATE messages SET session_id='second-session' WHERE message_id='m2'`},
		{"restore source", `UPDATE messages SET session_id='session' WHERE message_id='m2'`},
		{"remove full body", `DELETE FROM message_parts WHERE message_id='m1'`},
		{"restore full body", `INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('m1',0,'text','original')`},
		{"delete message", `DELETE FROM messages WHERE message_id='m2'`},
		{"hide tool role", `UPDATE messages SET role='tool' WHERE message_id='m1'`},
		{"restore assistant role", `UPDATE messages SET role='assistant' WHERE message_id='m1'`},
		{"hide reasoning envelope", `UPDATE messages SET raw_json='{"params":{"update":{"channel":"analysis"}}}' WHERE message_id='m1'`},
		{"restore visible envelope", `UPDATE messages SET raw_json='{}' WHERE message_id='m1'`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			_, mutationErr := store.db.ExecContext(t.Context(), mutation.query)
			require.NoError(t, mutationErr)
			requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
		})
	}
}

func TestSecretaryProgressProjectionPostgresGivenCleanCacheThenReadsWithoutLockingSourceTables(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	warm := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(
		t.Context(),
		`LOCK TABLE messages,message_parts IN ACCESS EXCLUSIVE MODE`,
	)
	require.NoError(t, err)
	locked, err := secretaryProgressWriter(t.Context(), tx)
	require.NoError(t, err)
	require.True(t, locked)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	page, err := store.ReadSecretaryProgress(ctx, "owner", domain.SecretaryProgressQuery{})
	require.NoError(t, err, "clean progress reads must not wait for either original-content table")
	page.AsOf = time.Time{}
	assert.Equal(t, warm, page)
	require.NoError(t, tx.Rollback())
	tx, err = store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	locked, err = secretaryProgressWriter(t.Context(), tx)
	require.NoError(t, err)
	require.True(t, locked)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed while writer busy' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	_, err = store.ReadSecretaryProgress(ctx, "owner", domain.SecretaryProgressQuery{})
	require.ErrorIs(t, err, domain.ErrSecretaryProgressPreparing,
		"dirty requests must defer instead of waiting for another projection writer")
	require.NoError(t, tx.Rollback())
	requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
}

func TestSecretaryProgressProjectionPostgresGivenScopeChangesThenNeverLeaksCachedOwnerCoverage(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash)
VALUES ('agent-node','owner','host','agent-node-key'),
('session-node','owner','host','session-node-key'),('message-node','owner','host','message-node-key');
UPDATE agents SET node_id='agent-node' WHERE agent_id='source';
UPDATE agent_sessions SET node_id='session-node' WHERE agent_id='source';
UPDATE messages SET node_id='message-node' WHERE message_id='m1';`)
	require.NoError(t, err)
	secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	_, err = store.PublishSecretaryJob(t.Context(), job, secretaryPublication(job))
	require.NoError(t, err)
	assert.Equal(
		t,
		1,
		requireSecretaryProgressProjectionMatchesSource(t, store, "owner").Counts.CompletedMessages,
	)
	requireSecretaryProgressProjectionMatchesSource(t, store, "other")
	for _, mutation := range []struct {
		name, query string
	}{
		{"transfer owner", `UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`},
		{"restore owner", `UPDATE agents SET owner_user_id='owner' WHERE agent_id='source'`},
		{"archive session", `UPDATE agent_sessions SET archived_at=NOW() WHERE agent_id='source'`},
		{"restore session", `UPDATE agent_sessions SET archived_at=NULL WHERE agent_id='source'`},
		{"mark internal source", `UPDATE agent_sessions SET source='secretary_internal' WHERE agent_id='source'`},
		{"restore source marker", `UPDATE agent_sessions SET source=NULL WHERE agent_id='source'`},
		{"register internal session", `INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES ('source','session','owner')`},
		{"unregister internal session", `DELETE FROM secretary_internal_sessions WHERE agent_id='source' AND session_id='session'`},
		{"delete agent node", `UPDATE nodes SET deleted_at=NOW() WHERE node_id='agent-node'`},
		{"restore agent node", `UPDATE nodes SET deleted_at=NULL WHERE node_id='agent-node'`},
		{"delete session node", `UPDATE nodes SET deleted_at=NOW() WHERE node_id='session-node'`},
		{"restore session node", `UPDATE nodes SET deleted_at=NULL WHERE node_id='session-node'`},
		{"delete message node", `UPDATE nodes SET deleted_at=NOW() WHERE node_id='message-node'`},
		{"restore message node", `UPDATE nodes SET deleted_at=NULL WHERE node_id='message-node'`},
		{"delete agent", `UPDATE agents SET deleted_at=NOW() WHERE agent_id='source'`},
		{"restore agent", `UPDATE agents SET deleted_at=NULL WHERE agent_id='source'`},
		{"update title", `UPDATE agent_sessions SET custom_session_name='Current custom title' WHERE agent_id='source'`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			_, mutationErr := store.db.ExecContext(t.Context(), mutation.query)
			require.NoError(t, mutationErr)
			requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
			requireSecretaryProgressProjectionMatchesSource(t, store, "other")
		})
	}
}

func TestSecretaryProgressProjectionPostgresGivenPaginationThenKeepsGlobalCountsAndOwnerIsolation(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO agent_sessions(id,agent_id,session_id,session_name)
VALUES (2,'source','page-2','Second'),(10,'source','page-10','Tenth'),(20,'other','foreign','Foreign');
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type)
VALUES ('page-2','source','page-2','test','inbound','user','chat'),
('page-10','source','page-10','test','inbound','user','chat'),
('foreign','other','foreign','test','inbound','user','chat');
INSERT INTO message_parts(message_id,part_index,part_type,text)
VALUES ('page-2',0,'text','second body'),('page-10',0,'text','tenth body'),('foreign',0,'text','foreign body');`)
	require.NoError(t, err)
	expected := secretaryProgressProjectionOracle(t, store, "owner")
	query := domain.SecretaryProgressQuery{Limit: 1}
	var ownerCursor string
	for i, id := range []string{"1", "10", "2"} {
		page, readErr := store.ReadSecretaryProgress(t.Context(), "owner", query)
		require.NoError(t, readErr)
		assert.Equal(t, expected.Counts, page.Counts)
		require.Len(t, page.Sessions, 1)
		assert.Equal(t, id, page.Sessions[0].SourceID)
		if i == 2 {
			assert.Empty(t, page.NextCursor)
		} else {
			require.NotEmpty(t, page.NextCursor)
			ownerCursor = page.NextCursor
		}
		query.Cursor = page.NextCursor
	}
	foreign := requireSecretaryProgressProjectionMatchesSource(t, store, "other")
	assert.Equal(t, 1, foreign.Counts.TotalMessages)
	require.Len(t, foreign.Sessions, 1)
	assert.Equal(t, "20", foreign.Sessions[0].SourceID)
	_, err = store.ReadSecretaryProgress(
		t.Context(),
		"other",
		domain.SecretaryProgressQuery{Cursor: ownerCursor},
	)
	assert.ErrorIs(t, err, domain.ErrInvalidCursor)
}

func secretaryProgressProjectionRows(t *testing.T, store *PostgresStore) int {
	t.Helper()
	var count int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_progress_messages`).
			Scan(&count),
	)
	return count
}

func TestSecretaryProgressProjectionPostgresGivenHistoricalBootstrapThenBoundsWorkAndResumesAfterRestart(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq,turn_id)
SELECT 'history_'||i,'source','session','test','inbound','user','chat',i+1,'turn-'||i
FROM generate_series(1,300) i;
INSERT INTO message_parts(message_id,part_index,part_type,text)
SELECT 'history_'||i,0,'text','historical message '||i FROM generate_series(1,300) i;
TRUNCATE secretary_progress_dirty,secretary_progress_messages,secretary_progress_sessions,secretary_progress_owners;`)
	require.NoError(t, err)
	page, err := store.ReadSecretaryProgress(t.Context(), "owner", domain.SecretaryProgressQuery{})
	require.ErrorIs(t, err, domain.ErrSecretaryProgressPreparing)
	assert.Empty(t, page.Sessions)
	assert.LessOrEqual(t, secretaryProgressProjectionRows(t, store), 256)
	var after, upper int64
	var complete bool
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT bootstrap_after,bootstrap_upper,bootstrap_complete
FROM secretary_progress_owners WHERE owner_user_id='owner'`).
			Scan(&after, &upper, &complete),
	)
	assert.Positive(t, after)
	assert.Less(t, after, upper)
	assert.False(t, complete)

	store = NewPostgresStore(store.db, time.Now)
	var retainedAfter int64
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT bootstrap_after
FROM secretary_progress_owners WHERE owner_user_id='owner'`).Scan(&retainedAfter))
	assert.Equal(t, after, retainedAfter)
	_, err = store.db.ExecContext(t.Context(), `
INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
VALUES ('live-message','source','session','test','inbound','user','chat',302);
INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('live-message',0,'text','live change');`)
	require.NoError(t, err)
	before := secretaryProgressProjectionRows(t, store)
	_, err = store.AdvanceSecretaryProgress(t.Context(), 1)
	require.NoError(t, err)
	assert.LessOrEqual(t, secretaryProgressProjectionRows(t, store)-before, 1)
	var live bool
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT EXISTS(
SELECT 1 FROM secretary_progress_messages WHERE owner_user_id='owner' AND message_id='live-message')`).Scan(&live))
	assert.True(t, live, "a live change must run before the remaining historical bootstrap")
	var beforeAfter, continuedAfter int64
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT bootstrap_after
FROM secretary_progress_owners WHERE owner_user_id='owner'`).Scan(&beforeAfter))
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='live continues' WHERE message_id='live-message'`,
	)
	require.NoError(t, err)
	before = secretaryProgressProjectionRows(t, store)
	_, err = store.AdvanceSecretaryProgress(t.Context(), 7)
	require.NoError(t, err)
	assert.LessOrEqual(t, secretaryProgressProjectionRows(t, store)-before, 6)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT bootstrap_after
FROM secretary_progress_owners WHERE owner_user_id='owner'`).Scan(&continuedAfter))
	assert.Greater(
		t,
		continuedAfter,
		beforeAfter,
		"live changes must leave unused batch capacity for history",
	)
	var liveHash string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT current_hash FROM secretary_progress_messages
WHERE owner_user_id='owner' AND message_id='live-message'`).Scan(&liveHash),
	)
	assert.Equal(t, domain.SearchContentHash("live continues"), liveHash)

	ready := false
	for range 100 {
		before = secretaryProgressProjectionRows(t, store)
		_, err = store.AdvanceSecretaryProgress(t.Context(), 7)
		require.NoError(t, err)
		assert.LessOrEqual(t, secretaryProgressProjectionRows(t, store)-before, 7)
		require.NoError(
			t,
			store.db.QueryRowContext(t.Context(), `SELECT bootstrap_complete AND NOT EXISTS(
SELECT 1 FROM secretary_progress_dirty WHERE owner_user_id='owner')
FROM secretary_progress_owners WHERE owner_user_id='owner'`).Scan(&ready),
		)
		if ready {
			break
		}
	}
	require.True(t, ready, "bounded background steps must finish the retained bootstrap")
	result := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, 302, result.Counts.PendingMessages)
	assert.Equal(t, 300, result.Counts.PendingTurns)
	assert.Equal(t, 2, result.Counts.UngroupedMessages)
}

func TestSecretaryProgressProjectionPostgresGivenPolicyChangeThenRebuildsCachedCanonicalCoverage(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	_, err := store.db.ExecContext(t.Context(), `
UPDATE secretary_progress_messages SET current_hash='obsolete',completed=TRUE;
UPDATE secretary_progress_sessions SET completed_messages=total_messages,pending_messages=0;
UPDATE secretary_progress_owners SET projection_version='obsolete-policy';`)
	require.NoError(t, err)
	page := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Zero(t, page.Counts.CompletedMessages)
	assert.Equal(t, 1, page.Counts.PendingMessages)
	var version, hash string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT projection_version
FROM secretary_progress_owners WHERE owner_user_id='owner'`).Scan(&version))
	assert.Equal(t, secretaryProgressProjectionVersion, version)
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT current_hash
FROM secretary_progress_messages WHERE owner_user_id='owner' AND message_id='m1'`).Scan(&hash))
	assert.Equal(t, domain.SearchContentHash("original"), hash)
}

func TestSecretaryProgressProjectionPostgresGivenCheckpointTransactionThenOnlyCommittedCoverageCounts(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	initial := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, 1, initial.Counts.PendingMessages)
	updateCheckpoint := func(tx *sql.Tx, hash string) {
		t.Helper()
		_, updateErr := tx.ExecContext(
			t.Context(),
			`UPDATE secretary_message_state SET processed_hash=$1,
processed_owner_user_id='owner',processed_canonicalization_version=$2 WHERE message_id='m1'`,
			hash,
			domain.SearchCanonicalizationVersion,
		)
		require.NoError(t, updateErr)
	}
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	updateCheckpoint(tx, domain.SearchContentHash("original"))
	beforeCommit := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, 1, beforeCommit.Counts.PendingMessages)
	require.NoError(t, tx.Rollback())
	rolledBack := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, initial, rolledBack)
	tx, err = store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	updateCheckpoint(tx, domain.SearchContentHash("original"))
	require.NoError(t, tx.Commit())
	committed := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Equal(t, 1, committed.Counts.CompletedMessages)
	tx, err = store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	updateCheckpoint(tx, "")
	require.NoError(t, tx.Rollback())
	assert.Equal(t, committed, requireSecretaryProgressProjectionMatchesSource(t, store, "owner"))
}

func secretaryProgressProjectionBarrier(
	t *testing.T,
	store *PostgresStore,
	ctx context.Context,
) (*sql.Tx, int) {
	t.Helper()
	barrier, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = barrier.Rollback() })
	var barrierPID int
	require.NoError(t, barrier.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&barrierPID))
	_, err = barrier.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
current_schema()||':progress-projection-test-barrier',0))`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
CREATE FUNCTION secretary_progress_test_barrier() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':progress-projection-test-barrier',0));
 RETURN NEW;
END $$;
CREATE TRIGGER secretary_progress_test_barrier BEFORE INSERT OR UPDATE ON secretary_progress_messages
FOR EACH ROW EXECUTE FUNCTION secretary_progress_test_barrier();`)
	require.NoError(t, err)
	return barrier, barrierPID
}

func requireSecretaryProgressProjectionBarrierWait(
	t *testing.T,
	store *PostgresStore,
	ctx context.Context,
	barrierPID int,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
WHERE $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory')`, barrierPID).Scan(&waiting)
		return queryErr == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
}

func TestSecretaryProgressProjectionPostgresGivenConcurrentSourceChangeThenDoesNotLoseDirtyGeneration(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	barrier, barrierPID := secretaryProgressProjectionBarrier(t, store, ctx)
	_, err := store.db.ExecContext(
		ctx,
		`UPDATE message_parts SET text='first change' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	refreshed := make(chan error, 1)
	go func() {
		_, refreshErr := NewPostgresStore(store.db, time.Now).AdvanceSecretaryProgress(ctx, 1)
		refreshed <- refreshErr
	}()
	requireSecretaryProgressProjectionBarrierWait(t, store, ctx, barrierPID)
	changed := make(chan error, 1)
	go func() {
		_, changeErr := store.db.ExecContext(
			ctx,
			`UPDATE message_parts SET text='concurrent change' WHERE message_id='m1'`,
		)
		changed <- changeErr
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
WHERE datname=current_database() AND query LIKE 'UPDATE message_parts SET text=''concurrent change''%'
AND wait_event_type='Lock')`).Scan(&waiting)
		return queryErr == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, barrier.Commit())
	require.NoError(t, <-refreshed)
	require.NoError(t, <-changed)
	var queued bool
	require.NoError(
		t,
		store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secretary_progress_dirty
WHERE owner_user_id='owner' AND message_id='m1')`).Scan(&queued),
	)
	assert.True(t, queued, "the change committed after refresh must retain its dirty identity")
	requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	var hash string
	require.NoError(
		t,
		store.db.QueryRowContext(ctx, `SELECT current_hash FROM secretary_progress_messages
WHERE owner_user_id='owner' AND message_id='m1'`).Scan(&hash),
	)
	assert.Equal(t, domain.SearchContentHash("concurrent change"), hash)
}

func TestSecretaryProgressProjectionPostgresGivenInitialBootstrapOwnerMoveThenRetainsBothOwnersDirty(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO agent_sessions(agent_id,session_id) VALUES ('other','foreign-session');
TRUNCATE secretary_progress_dirty,secretary_progress_messages,secretary_progress_sessions,secretary_progress_owners;`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO secretary_progress_owners(owner_user_id,projection_version,bootstrap_upper)
SELECT 'owner',$1,max(id) FROM messages;`,
		secretaryProgressProjectionVersion,
	)
	require.NoError(t, err)
	_, err = store.AdvanceSecretaryProgress(t.Context(), 1)
	require.NoError(t, err)
	assert.Zero(t, secretaryProgressProjectionRows(t, store),
		"historical discovery must commit its dirty route before canonicalizing any source")
	var staged bool
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM secretary_progress_dirty
WHERE owner_user_id='owner' AND message_id='m1' AND is_backfill)`).Scan(&staged),
	)
	require.True(t, staged)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	barrier, barrierPID := secretaryProgressProjectionBarrier(t, store, ctx)
	refreshed := make(chan error, 1)
	go func() {
		_, refreshErr := NewPostgresStore(store.db, time.Now).AdvanceSecretaryProgress(ctx, 1)
		refreshed <- refreshErr
	}()
	requireSecretaryProgressProjectionBarrierWait(t, store, ctx, barrierPID)
	moved := make(chan error, 1)
	go func() {
		_, moveErr := store.db.ExecContext(
			ctx,
			`UPDATE messages SET agent_id='other',session_id='foreign-session' WHERE message_id='m1'`,
		)
		moved <- moveErr
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
WHERE datname=current_database() AND query LIKE 'UPDATE messages SET agent_id=''other'',session_id=''foreign-session''%'
AND wait_event_type='Lock')`).Scan(&waiting)
		return queryErr == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, barrier.Commit())
	require.NoError(t, <-refreshed)
	require.NoError(t, <-moved)
	var owners int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(DISTINCT owner_user_id)
FROM secretary_progress_dirty WHERE message_id='m1' AND owner_user_id IN ('owner','other')`).Scan(&owners))
	assert.Equal(t, 2, owners, "the newly visible and prior queued owner must both be invalidated")
	old := requireSecretaryProgressProjectionMatchesSource(t, store, "owner")
	assert.Zero(t, old.Counts.TotalMessages)
	current := requireSecretaryProgressProjectionMatchesSource(t, store, "other")
	assert.Equal(t, 1, current.Counts.PendingMessages)
	require.Len(t, current.Sessions, 1)
	assert.Equal(t, "foreign-session", current.Sessions[0].SessionID)
}

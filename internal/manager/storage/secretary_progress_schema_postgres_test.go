package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretaryProgressDirtyKeys(t *testing.T, store *PostgresStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(t.Context(), `SELECT owner_user_id||':'||message_id
 FROM secretary_progress_dirty ORDER BY owner_user_id,message_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	keys := []string{}
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	return keys
}

func secretaryProgressClearDirty(t *testing.T, store *PostgresStore) {
	t.Helper()
	_, err := store.db.ExecContext(t.Context(), `TRUNCATE secretary_progress_dirty`)
	require.NoError(t, err)
}

func TestSecretaryProgressSchemaGivenDisabledSecretaryThenPartsStillEnqueueCoalescedWork(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	var settings int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_settings`).
			Scan(&settings),
	)
	assert.Zero(t, settings)
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	var before, after int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT generation FROM secretary_progress_dirty`).
			Scan(&before),
	)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_progress_dirty SET is_backfill=TRUE;
 UPDATE message_parts SET text='changed' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT generation FROM secretary_progress_dirty`).
			Scan(&after),
	)
	assert.Greater(t, after, before)
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	var backfill bool
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT is_backfill FROM secretary_progress_dirty`).
			Scan(&backfill),
	)
	assert.False(t, backfill, "a live change must promote already queued historical work")
	var projections int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_progress_messages`).
			Scan(&projections),
	)
	assert.Zero(t, projections, "feed must not hash or create a source projection inline")
}

func TestSecretaryProgressSchemaGivenSearchAcknowledgementThenIgnoresIndexWorkerWrites(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryProgressClearDirty(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE search_sync_state SET indexed_revision=desired_revision,
 lease_token='indexer',lease_until=NOW(),attempts=attempts+1,available_at=NOW(),last_error='retry';
 UPDATE search_sync_state SET desired_revision=desired_revision`,
	)
	require.NoError(t, err)
	assert.Empty(t, secretaryProgressDirtyKeys(t, store))
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE search_sync_state SET desired_revision=desired_revision+1
 WHERE kind='message' AND source_id='m1'`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(t.Context(), `SELECT pax_search_mark_dirty('session','1');
 SELECT pax_secretary_progress_enqueue(NULL); SELECT pax_secretary_progress_enqueue('')`)
	require.NoError(t, err)
	assert.Empty(t, secretaryProgressDirtyKeys(t, store))
}

func TestSecretaryProgressSchemaGivenPartMovesAndDisplayTargetsThenEnqueuesEveryAffectedMessage(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type) VALUES
 ('m2','source','session','test','inbound','user','chat'),
 ('display','source','session','test','inbound','tool','pax:artifact')`)
	require.NoError(t, err)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET message_id='m2' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1", "owner:m2"}, secretaryProgressDirtyKeys(t, store))
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(t.Context(), `UPDATE messages SET parent_message_id='m1',
 raw_json='{"replaces_message_ids":["m2"]}' WHERE message_id='display'`)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"owner:display", "owner:m1", "owner:m2"},
		secretaryProgressDirtyKeys(t, store),
	)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='display'`)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1", "owner:m2"}, secretaryProgressDirtyKeys(t, store))
}

func TestSecretaryProgressSchemaGivenOwnerTransferOrDeleteThenRetainsOldProjectionCleanup(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO secretary_progress_messages
 (owner_user_id,message_id,source_id,current_hash,canonicalization_version,completed)
 SELECT 'owner','m1',id,'hash','canonical',TRUE FROM agent_sessions WHERE agent_id='source';`)
	require.NoError(t, err)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"other:m1", "owner:m1"}, secretaryProgressDirtyKeys(t, store))
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m1'`)
	require.NoError(t, err)
	assert.Contains(t, secretaryProgressDirtyKeys(t, store), "owner:m1")
	var remaining int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_progress_messages`).
			Scan(&remaining),
	)
	assert.Equal(
		t,
		1,
		remaining,
		"source deletion must leave the old projection for asynchronous subtraction",
	)
}

func TestSecretaryProgressSchemaGivenCheckpointChangesThenIgnoresSchedulingMetadata(t *testing.T) {
	store := secretaryPostgresFixture(t)
	secretaryProgressClearDirty(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO secretary_jobs(source_id,owner_user_id,source_agent_id,source_session_id)
 SELECT id,'owner','source','session' FROM agent_sessions WHERE agent_id='source';
 INSERT INTO secretary_message_state(source_id,message_id,change_revision)
 SELECT id,'m1',1 FROM agent_sessions WHERE agent_id='source'`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_message_state SET change_revision=change_revision+1,
 last_batch_id='batch',processing_result='{"ok":true}',processed_hash=processed_hash`,
	)
	require.NoError(t, err)
	assert.Empty(t, secretaryProgressDirtyKeys(t, store))
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type)
 VALUES('m2','source','session','test','inbound','user','chat')`)
	require.NoError(t, err)
	for _, target := range []string{"m2", "m1"} {
		secretaryProgressClearDirty(t, store)
		_, err = store.db.ExecContext(
			t.Context(),
			`UPDATE secretary_message_state SET message_id=$1`,
			target,
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"owner:m1", "owner:m2"}, secretaryProgressDirtyKeys(t, store))
	}
	secretaryProgressClearDirty(t, store)
	for _, statement := range []string{
		`UPDATE secretary_message_state SET processed_hash='new-hash'`,
		`UPDATE secretary_message_state SET processed_owner_user_id='owner'`,
		`UPDATE secretary_message_state SET processed_canonicalization_version='new-canonical'`,
		`DELETE FROM secretary_message_state`,
	} {
		_, err = store.db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
		assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
		secretaryProgressClearDirty(t, store)
	}
}

func TestSecretaryProgressSchemaGivenInternalRegistryMoveThenQueuesOldNewAndDeletedSources(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('source','next');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type)
 VALUES('m2','source','next','test','inbound','user','chat');
 INSERT INTO secretary_progress_messages(owner_user_id,message_id,source_id,current_hash,canonicalization_version,completed)
 SELECT 'owner',CASE WHEN session_id='session' THEN 'm1' ELSE 'm2' END,id,'hash','canonical',FALSE
 FROM agent_sessions WHERE agent_id='source';
 INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES('source','session','owner')`,
	)
	require.NoError(t, err)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_internal_sessions SET session_id='next'`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1", "owner:m2"}, secretaryProgressDirtyKeys(t, store))
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_internal_sessions SET session_id=session_id`,
	)
	require.NoError(t, err)
	assert.Empty(t, secretaryProgressDirtyKeys(t, store))
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='m2'`)
	require.NoError(t, err)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM secretary_internal_sessions`)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m2"}, secretaryProgressDirtyKeys(t, store))
}

func TestSecretaryProgressSchemaGivenInternalRegistrationThenQueuesCachedRemovalAndUnregistration(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO secretary_progress_messages
 (owner_user_id,message_id,source_id,current_hash,canonicalization_version,completed)
 SELECT 'owner','m1',id,'hash','canonical',FALSE FROM agent_sessions WHERE agent_id='source'`)
	require.NoError(t, err)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id)
 VALUES('source','session','owner')`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM secretary_progress_messages`)
	require.NoError(t, err)
	secretaryProgressClearDirty(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='internal body' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	assert.Empty(t, secretaryProgressDirtyKeys(t, store))
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM secretary_internal_sessions`)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	for _, statement := range []string{
		`UPDATE agent_sessions SET source='secretary_internal' WHERE agent_id='source'`,
		`UPDATE messages SET session_id='sess_secretary_hidden' WHERE message_id='m1';
 UPDATE agent_sessions SET source=NULL,session_id='sess_secretary_hidden' WHERE agent_id='source'`,
	} {
		secretaryProgressClearDirty(t, store)
		_, err = store.db.ExecContext(t.Context(), statement)
		require.NoError(t, err)
		assert.Empty(t, secretaryProgressDirtyKeys(t, store))
	}
}

func TestSecretaryProgressSchemaGivenRepeatedMigrationThenPreservesProjectionAndQueue(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO secretary_progress_owners
 (owner_user_id,projection_version,bootstrap_after,bootstrap_upper,bootstrap_complete)
 VALUES('owner','v1',2,10,FALSE);
 INSERT INTO secretary_progress_sessions(owner_user_id,source_id,total_messages,pending_messages)
 SELECT 'owner',id,1,1 FROM agent_sessions WHERE agent_id='source';
 INSERT INTO secretary_progress_messages(owner_user_id,message_id,source_id,current_hash,canonicalization_version,completed)
 SELECT 'owner','m1',id,'hash','canonical',FALSE FROM agent_sessions WHERE agent_id='source'`)
	require.NoError(t, err)
	const snapshot = `SELECT jsonb_build_object(
 'owners',(SELECT jsonb_agg(to_jsonb(p)) FROM secretary_progress_owners p),
 'messages',(SELECT jsonb_agg(to_jsonb(p)) FROM secretary_progress_messages p),
 'sessions',(SELECT jsonb_agg(to_jsonb(p)) FROM secretary_progress_sessions p),
 'dirty',(SELECT jsonb_agg(to_jsonb(p)) FROM secretary_progress_dirty p))::text`
	var before, after string
	require.NoError(t, store.db.QueryRowContext(t.Context(), snapshot).Scan(&before))
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "secretary.sql"))
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), string(raw))
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRowContext(t.Context(), snapshot).Scan(&after))
	assert.JSONEq(t, before, after)
}

func TestSecretaryProgressSchemaGivenInFlightFirstProjectionThenDeletionCannotLoseItsTombstone(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	writer, err := store.db.Conn(ctx)
	require.NoError(t, err)
	worker, err := store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	require.NoError(t, err)
	defer func() {
		cancel()
		_ = worker.Rollback()
		require.NoError(t, writer.Close())
	}()
	var workerPID, writerPID int
	require.NoError(t, worker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&workerPID))
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID))
	var claimed int64
	require.NoError(t, worker.QueryRowContext(ctx, `SELECT generation FROM secretary_progress_dirty
 WHERE owner_user_id='owner' AND message_id='m1' FOR UPDATE`).Scan(&claimed))
	var source int64
	require.NoError(t, worker.QueryRowContext(ctx, `SELECT s.id FROM messages m
 JOIN agent_sessions s ON s.agent_id=m.agent_id AND s.session_id=m.session_id WHERE m.message_id='m1'`).Scan(&source))
	deleted := make(chan error, 1)
	go func() {
		_, deleteErr := writer.ExecContext(ctx, `DELETE FROM messages WHERE message_id='m1'`)
		deleted <- deleteErr
	}()
	awaitSecretaryProgressDeleteFence(t, ctx, store, workerPID, writerPID, deleted)
	_, err = worker.ExecContext(ctx, `INSERT INTO secretary_progress_messages
 (owner_user_id,message_id,source_id,current_hash,canonicalization_version,completed)
 VALUES('owner','m1',$1,'old-hash','canonical',FALSE)`, source)
	require.NoError(t, err)
	_, err = worker.ExecContext(ctx, `DELETE FROM secretary_progress_dirty
 WHERE owner_user_id='owner' AND message_id='m1' AND generation=$1`, claimed)
	require.NoError(t, err)
	require.NoError(t, worker.Commit())
	select {
	case err = <-deleted:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("source deletion did not complete after the projection transaction committed")
	}
	assert.Equal(t, []string{"owner:m1"}, secretaryProgressDirtyKeys(t, store))
	var next int64
	require.NoError(
		t,
		store.db.QueryRowContext(ctx, `SELECT generation FROM secretary_progress_dirty`).
			Scan(&next),
	)
	assert.Greater(t, next, claimed)
}

func awaitSecretaryProgressDeleteFence(
	t *testing.T,
	ctx context.Context,
	store *PostgresStore,
	workerPID, writerPID int,
	deleted <-chan error,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		require.NoError(t, store.db.QueryRowContext(
			ctx,
			`SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`,
			workerPID,
			writerPID,
		).Scan(&blocked))
		if blocked {
			return
		}
		select {
		case err := <-deleted:
			require.NoError(t, err)
			t.Fatal(
				"deletion escaped the committed dirty-row fence before the first projection was published",
			)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("source deletion did not reach the expected projection queue lock")
		}
	}
}

func TestSecretaryProgressSchemaGivenMessageIdentityChangesThenRefreshesBothDirtyIdentities(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `DELETE FROM message_parts WHERE message_id='m1'`)
	require.NoError(t, err)
	var before, after int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT generation FROM secretary_progress_dirty`).
			Scan(&before),
	)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE messages SET message_id='renamed' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"owner:m1", "owner:renamed"}, secretaryProgressDirtyKeys(t, store))
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT generation FROM secretary_progress_dirty
 WHERE message_id='m1'`).Scan(&after),
	)
	assert.Greater(t, after, before, "the old identity must retain a fresh removal event")
}

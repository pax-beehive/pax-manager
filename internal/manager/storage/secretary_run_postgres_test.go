package storage

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryRunPostgresFixture(t *testing.T, count int) *PostgresStore {
	t.Helper()
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	_, err := store.db.ExecContext(t.Context(), `DELETE FROM secretary_jobs`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO agent_sessions(agent_id,session_id)
 SELECT 'source','bulk_'||i FROM generate_series(1,$1::integer) i`, count)
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO secretary_jobs(source_id,owner_user_id,source_agent_id,source_session_id,
 desired_revision,processed_revision,available_at,attempts,last_error,auto_retry_key,auto_retry_count)
 SELECT id,'owner',agent_id,session_id,2,1,NOW()+INTERVAL '1 day',3,'previous failure','retry-key',2
 FROM agent_sessions WHERE agent_id='source' AND starts_with(session_id,'bulk_')`,
	)
	require.NoError(t, err)
	return store
}

func secretaryRunCommand(
	t *testing.T,
	store *PostgresStore,
	owner, id string,
) (string, int64, int64) {
	t.Helper()
	var state string
	var after, queued int64
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT state,source_after,queued_count
 FROM secretary_run_commands WHERE owner_user_id=$1 AND request_id=$2`, owner, id).Scan(&state, &after, &queued))
	return state, after, queued
}

func secretaryRunJobsSnapshot(t *testing.T, store *PostgresStore) string {
	t.Helper()
	var snapshot string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT COALESCE(jsonb_object_agg(
 source_session_id,to_jsonb(j)),'{}'::jsonb)::text FROM secretary_jobs j`).Scan(&snapshot))
	return snapshot
}

func secretaryRunComplete(t *testing.T, store *PostgresStore, owner, id string, limit int) int64 {
	t.Helper()
	for range 100 {
		state, _, count := secretaryRunCommand(t, store, owner, id)
		if state == "completed" {
			return count
		}
		_, err := store.AdvanceSecretaryRuns(t.Context(), limit)
		require.NoError(t, err)
	}
	t.Fatal("bulk command did not complete within bounded metadata pages")
	return 0
}

func TestSecretaryRunPostgresGivenLockedRawSourcesThenAcceptanceAndMetadataAdvanceRemainAvailable(
	t *testing.T,
) {
	store := secretaryRunPostgresFixture(t, 1)
	blocker, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	_, err = blocker.ExecContext(
		t.Context(),
		`LOCK TABLE messages,message_parts,secretary_message_state,
 summary_versions,summary_sources IN ACCESS EXCLUSIVE MODE`,
	)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	receipt, err := store.RequestSecretaryRun(ctx, "owner", "locked-raw")
	require.NoError(t, err)
	assert.Equal(t, "locked-raw", receipt.RequestID)
	assert.Equal(t, "accepted", receipt.State)
	assert.False(t, receipt.AcceptedAt.IsZero())
	_, err = store.AdvanceSecretaryRuns(ctx, 128)
	require.NoError(t, err)
	state, _, queued := secretaryRunCommand(t, store, "owner", "locked-raw")
	assert.Equal(t, "completed", state)
	assert.EqualValues(t, 1, queued)
}

func TestSecretaryRunPostgresGivenConcurrentDuplicateAndLostResponseThenReceiptIsImmutableAndOwnerBound(
	t *testing.T,
) {
	store := secretaryRunPostgresFixture(t, 1)
	const callers = 8
	results := make([]domain.SecretaryRunReceipt, callers)
	errs := make([]error, callers)
	var ready sync.WaitGroup
	for i := range callers {
		ready.Add(1)
		go func() {
			defer ready.Done()
			results[i], errs[i] = store.RequestSecretaryRun(t.Context(), "owner", "same-id")
		}()
	}
	ready.Wait()
	for i := range callers {
		require.NoError(t, errs[i])
		assert.Equal(t, results[0], results[i])
	}
	for i := range callers {
		ready.Add(1)
		go func() {
			defer ready.Done()
			_, errs[i] = store.AdvanceSecretaryRuns(t.Context(), 2)
		}()
	}
	ready.Wait()
	for _, advanceErr := range errs {
		require.NoError(t, advanceErr)
	}
	assert.EqualValues(t, 1, secretaryRunComplete(t, store, "owner", "same-id", 2))
	before := secretaryRunJobsSnapshot(t, store)
	_, err := store.RequestSecretaryRun(t.Context(), "other", "same-id")
	assert.ErrorIs(
		t,
		err,
		ErrConflict,
		"another owner must not replay a receipt before enabling Secretary",
	)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_settings SET enabled=FALSE,revision=revision+1
 WHERE owner_user_id='owner';
 INSERT INTO secretary_settings(owner_user_id,agent_id,enabled,revision) VALUES('other','other',TRUE,9)`,
	)
	require.NoError(t, err)
	replayed, err := store.RequestSecretaryRun(t.Context(), "owner", "same-id")
	require.NoError(t, err)
	assert.Equal(
		t,
		results[0],
		replayed,
		"a lost response must replay even after completion and disable",
	)
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "new-id")
	assert.ErrorIs(t, err, ErrConflict)
	other, err := store.RequestSecretaryRun(t.Context(), "other", "same-id")
	require.NoError(t, err)
	assert.Equal(t, "same-id", other.RequestID)
	var commands int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_run_commands
 WHERE request_id='same-id'`).Scan(&commands),
	)
	assert.Equal(t, 2, commands)
	_, err = store.AdvanceSecretaryRuns(t.Context(), 128)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		before,
		secretaryRunJobsSnapshot(t, store),
		"replay must not issue a second retry",
	)
}

func TestSecretaryRunPostgresGivenReceiptFailureThenNoWorkEscapesAndRetryCanBeAccepted(
	t *testing.T,
) {
	store := secretaryRunPostgresFixture(t, 1)
	before := secretaryRunJobsSnapshot(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`CREATE FUNCTION reject_run_receipt() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'reject synthetic receipt'; END $$;
 CREATE TRIGGER reject_run_receipt BEFORE INSERT ON secretary_run_commands
 FOR EACH ROW EXECUTE FUNCTION reject_run_receipt()`,
	)
	require.NoError(t, err)
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "receipt-retry")
	require.Error(t, err)
	var commands int
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM secretary_run_commands`).
			Scan(&commands),
	)
	assert.Zero(t, commands)
	assert.JSONEq(t, before, secretaryRunJobsSnapshot(t, store))
	_, err = store.db.ExecContext(
		t.Context(),
		`DROP TRIGGER reject_run_receipt ON secretary_run_commands`,
	)
	require.NoError(t, err)
	receipt, err := store.RequestSecretaryRun(t.Context(), "owner", "receipt-retry")
	require.NoError(t, err)
	assert.Equal(t, "accepted", receipt.State)
	assert.JSONEq(
		t,
		before,
		secretaryRunJobsSnapshot(t, store),
		"acceptance must not update job metadata",
	)
}

func TestSecretaryRunPostgresGivenWorkerFailureThenCursorAndJobsRollbackTogether(t *testing.T) {
	store := secretaryRunPostgresFixture(t, 2)
	_, err := store.RequestSecretaryRun(t.Context(), "owner", "rollback")
	require.NoError(t, err)
	before := secretaryRunJobsSnapshot(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`CREATE FUNCTION reject_run_job() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN IF NEW.source_session_id='bulk_2' THEN RAISE EXCEPTION 'reject synthetic retry'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_run_job BEFORE UPDATE ON secretary_jobs FOR EACH ROW EXECUTE FUNCTION reject_run_job()`,
	)
	require.NoError(t, err)
	_, err = store.AdvanceSecretaryRuns(t.Context(), 128)
	require.Error(t, err)
	state, after, queued := secretaryRunCommand(t, store, "owner", "rollback")
	assert.Equal(t, "pending", state)
	assert.Zero(t, after)
	assert.Zero(t, queued)
	assert.JSONEq(t, before, secretaryRunJobsSnapshot(t, store))
	_, err = store.db.ExecContext(t.Context(), `DROP TRIGGER reject_run_job ON secretary_jobs`)
	require.NoError(t, err)
	assert.EqualValues(t, 2, secretaryRunComplete(t, store, "owner", "rollback", 2))
}

func TestSecretaryRunPostgresGivenProtectedJobsThenRetriesOnlyCurrentSafePendingWork(t *testing.T) {
	store := secretaryRunPostgresFixture(t, 15)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET paused=TRUE WHERE source_session_id='bulk_2';
 UPDATE secretary_jobs SET lease_token='active',lease_until=NOW()+INTERVAL '1 day' WHERE source_session_id='bulk_3';
 UPDATE secretary_jobs SET desired_revision=processed_revision WHERE source_session_id='bulk_4';
 INSERT INTO secretary_batches(batch_id,source_id,owner_user_id,input_json,settings_revision,retry_epoch,through_revision,state)
 SELECT 'protected_'||j.source_session_id,j.source_id,'owner','{}',cfg.revision,0,j.desired_revision,
 CASE j.source_session_id WHEN 'bulk_5' THEN 'unknown' WHEN 'bulk_6' THEN 'pending'
 WHEN 'bulk_7' THEN 'running' ELSE 'output_received' END
 FROM secretary_jobs j JOIN secretary_settings cfg ON cfg.owner_user_id=j.owner_user_id
 WHERE source_session_id IN ('bulk_5','bulk_6','bulk_7','bulk_8');
 UPDATE agent_sessions SET source='secretary_internal' WHERE session_id='bulk_9';
 INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES('source','bulk_10','owner');
 UPDATE agent_sessions SET archived_at=NOW() WHERE session_id='bulk_11';
 UPDATE secretary_jobs SET source_agent_id='other' WHERE source_session_id='bulk_12';
 INSERT INTO nodes(node_id,owner_user_id,hostname,os,api_key_hash,deleted_at)
 VALUES('deleted-node','owner','host','linux','deleted-node-key',NOW());
 UPDATE agent_sessions SET node_id='deleted-node' WHERE session_id='bulk_13';
 UPDATE secretary_jobs SET lease_token='expired',lease_until=NOW()-INTERVAL '1 hour' WHERE source_session_id='bulk_14';
 INSERT INTO secretary_batches(batch_id,source_id,owner_user_id,input_json,settings_revision,retry_epoch,through_revision,state)
 SELECT 'stale_unknown',j.source_id,'owner','{}',cfg.revision-1,0,j.desired_revision,'unknown'
 FROM secretary_jobs j JOIN secretary_settings cfg ON cfg.owner_user_id=j.owner_user_id WHERE source_session_id='bulk_15'`,
	)
	require.NoError(t, err)
	var protectedBefore, batchesBefore string
	const protectedSnapshot = `SELECT jsonb_object_agg(source_session_id,to_jsonb(j))::text
 FROM secretary_jobs j WHERE source_session_id NOT IN ('bulk_1','bulk_14')`
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), protectedSnapshot).Scan(&protectedBefore),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT jsonb_agg(to_jsonb(b) ORDER BY batch_id)::text
 FROM secretary_batches b`).Scan(&batchesBefore),
	)
	_, err = store.RequestSecretaryRun(t.Context(), "owner", "protected")
	require.NoError(t, err)
	assert.EqualValues(t, 2, secretaryRunComplete(t, store, "owner", "protected", 2))
	var protectedAfter, batchesAfter string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), protectedSnapshot).Scan(&protectedAfter),
	)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT jsonb_agg(to_jsonb(b) ORDER BY batch_id)::text
 FROM secretary_batches b`).Scan(&batchesAfter),
	)
	assert.JSONEq(
		t,
		protectedBefore,
		protectedAfter,
		"paused, leased, unresolved and inaccessible jobs must remain byte-for-byte unchanged",
	)
	assert.JSONEq(t, batchesBefore, batchesAfter)
	rows, err := store.db.QueryContext(
		t.Context(),
		`SELECT desired_revision,processed_revision,available_at<=NOW()
	 FROM secretary_jobs WHERE source_session_id IN ('bulk_1','bulk_14')`,
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var desired, processed int64
		var available bool
		require.NoError(t, rows.Scan(&desired, &processed, &available))
		assert.EqualValues(t, 2, desired)
		assert.EqualValues(t, 1, processed)
		assert.True(t, available)
	}
	require.NoError(t, rows.Err())
}

func TestSecretaryRunPostgresGivenBoundedPagesThenUpperBoundAndRepeatedRequestStayStable(
	t *testing.T,
) {
	store := secretaryRunPostgresFixture(t, 5)
	first, err := store.RequestSecretaryRun(t.Context(), "owner", "pages")
	require.NoError(t, err)
	_, err = store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id) VALUES('source','late');
 INSERT INTO secretary_jobs(source_id,owner_user_id,source_agent_id,source_session_id,desired_revision,available_at)
 SELECT id,'owner',agent_id,session_id,2,NOW()+INTERVAL '1 day' FROM agent_sessions WHERE session_id='late'`,
	)
	require.NoError(t, err)
	_, err = store.AdvanceSecretaryRuns(t.Context(), 2)
	require.NoError(t, err)
	state, _, queued := secretaryRunCommand(t, store, "owner", "pages")
	assert.Equal(t, "pending", state)
	assert.EqualValues(t, 2, queued)
	assert.EqualValues(t, 5, secretaryRunComplete(t, store, "owner", "pages", 2))
	var untouched bool
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT available_at>NOW() FROM secretary_jobs
 WHERE source_session_id='late'`).Scan(&untouched),
	)
	assert.True(t, untouched, "new sources created after acceptance belong to a later command")
	before := secretaryRunJobsSnapshot(t, store)
	replay, err := store.RequestSecretaryRun(t.Context(), "owner", "pages")
	require.NoError(t, err)
	assert.Equal(t, first, replay)
	_, err = store.AdvanceSecretaryRuns(t.Context(), 128)
	require.NoError(t, err)
	assert.JSONEq(t, before, secretaryRunJobsSnapshot(t, store))
}

func TestSecretaryRunPostgresGivenSettingsRevisionChangeThenCancelsWithoutRetargeting(
	t *testing.T,
) {
	store := secretaryRunPostgresFixture(t, 2)
	first, err := store.RequestSecretaryRun(t.Context(), "owner", "config-change")
	require.NoError(t, err)
	before := secretaryRunJobsSnapshot(t, store)
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_settings SET revision=revision+1 WHERE owner_user_id='owner'`,
	)
	require.NoError(t, err)
	_, err = store.AdvanceSecretaryRuns(t.Context(), 128)
	require.NoError(t, err)
	state, _, queued := secretaryRunCommand(t, store, "owner", "config-change")
	assert.Equal(t, "canceled", state)
	assert.Zero(t, queued)
	assert.JSONEq(t, before, secretaryRunJobsSnapshot(t, store))
	replayed, err := store.RequestSecretaryRun(t.Context(), "owner", "config-change")
	require.NoError(t, err)
	assert.Equal(t, first, replayed)
}

func TestSecretaryRunPostgresGivenLockedFirstJobThenCannotAdvanceCursorPastUnprocessedWork(
	t *testing.T,
) {
	store := secretaryRunPostgresFixture(t, 2)
	_, err := store.RequestSecretaryRun(t.Context(), "owner", "locked-first")
	require.NoError(t, err)
	blocker, err := store.db.BeginTx(t.Context(), &sql.TxOptions{})
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	var source int64
	require.NoError(t, blocker.QueryRowContext(t.Context(), `SELECT source_id FROM secretary_jobs
 WHERE source_session_id='bulk_1' FOR UPDATE`).Scan(&source))
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	_, err = store.AdvanceSecretaryRuns(ctx, 2)
	require.Error(t, err, "a locked first metadata row must not be skipped while moving the cursor")
	state, after, queued := secretaryRunCommand(t, store, "owner", "locked-first")
	assert.Equal(t, "pending", state)
	assert.Zero(t, after)
	assert.Zero(t, queued)
	require.NoError(t, blocker.Rollback())
	assert.EqualValues(t, 2, secretaryRunComplete(t, store, "owner", "locked-first", 2))
	assert.Positive(t, source)
}

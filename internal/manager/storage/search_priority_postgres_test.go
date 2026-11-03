package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func searchPriorityStore(t *testing.T) *PostgresStore {
	t.Helper()
	store := searchIntegrationStore(t)
	_, err := store.db.ExecContext(t.Context(), `TRUNCATE search_sync_state`)
	require.NoError(t, err)
	return store
}

func seedSearchHistory(t *testing.T, store *PostgresStore, count int) {
	t.Helper()
	// Missing sources are valid tombstone work and keep this test focused on scheduling.
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO search_sync_state(kind,source_id,available_at)
 SELECT 'message','history-'||i,'2000-01-01'::timestamptz FROM generate_series(1,$1::int) i`,
		count,
	)
	require.NoError(t, err)
}

func seedSearchLive(t *testing.T, store *PostgresStore, start, count int) {
	t.Helper()
	_, err := store.db.ExecContext(t.Context(), `SELECT pax_search_mark_dirty('message','live-'||i)
 FROM generate_series($1::int,$2::int) i`, start, start+count-1)
	require.NoError(t, err)
}

func countSearchClasses(t *testing.T, store *PostgresStore, work []domain.SearchWork) (int, int) {
	t.Helper()
	live, history := 0, 0
	for _, item := range work {
		var background bool
		require.NoError(
			t,
			store.db.QueryRowContext(t.Context(), `SELECT is_backfill FROM search_sync_state
 WHERE kind=$1 AND source_id=$2`, item.Document.Kind, item.Document.SourceID).Scan(&background),
		)
		if background {
			history++
		} else {
			live++
		}
	}
	return live, history
}

func TestSearchPriorityPostgresGivenHistoricalMessagesThenTitlesAndSummariesDoNotWaitForDrain(
	t *testing.T,
) {
	store := searchPriorityStore(t)
	seedSearchHistory(t, store, 100)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO search_sync_state(kind,source_id,available_at)
 VALUES ('session','1','2000-01-01'),('summary','summary-history','2000-01-01')`,
	)
	require.NoError(t, err)
	work, err := store.ClaimSearchBatch(t.Context(), 20, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 20)
	ids := make([]string, 0, len(work))
	for _, item := range work {
		ids = append(ids, item.Document.DocumentID())
	}
	assert.Contains(t, ids, "session:1")
	assert.Contains(t, ids, "summary:summary-history")
}

func TestSearchPriorityPostgresGivenContinuousLiveWritesThenReservesHistoryWithoutBlockingLive(
	t *testing.T,
) {
	store := searchPriorityStore(t)
	seedSearchHistory(t, store, 100)
	for round := range 3 {
		seedSearchLive(t, store, round*100, 30)
		work, err := store.ClaimSearchBatch(t.Context(), 20, time.Minute)
		require.NoError(t, err)
		require.Len(t, work, 20)
		live, history := countSearchClasses(t, store, work)
		assert.Equal(t, 16, live)
		assert.Equal(t, 4, history)
		foundFirst := false
		for _, item := range work {
			if item.Document.SourceID == "live-0" {
				foundFirst = true
				assert.EqualValues(
					t,
					1,
					item.Document.Revision,
					"new live work must not require revision two",
				)
			}
			require.NoError(t, store.CompleteSearchWork(t.Context(), item, nil))
		}
		if round == 0 {
			assert.True(
				t,
				foundFirst,
				"a new revision-one write must pass older backfill immediately",
			)
		}
	}
}

func TestSearchPriorityPostgresGivenUnevenLanesThenBorrowsAllUnusedCapacity(t *testing.T) {
	for _, test := range []struct{ live, history, limit, wantLive, wantHistory int }{
		{30, 0, 20, 20, 0}, {0, 30, 20, 0, 20}, {3, 30, 20, 3, 17},
		{30, 2, 20, 18, 2}, {3, 2, 20, 3, 2}, {1, 1, 1, 1, 0}, {0, 1, 1, 0, 1},
	} {
		t.Run(
			fmt.Sprintf("live_%d_history_%d_limit_%d", test.live, test.history, test.limit),
			func(t *testing.T) {
				store := searchPriorityStore(t)
				seedSearchHistory(t, store, test.history)
				seedSearchLive(t, store, 0, test.live)
				work, err := store.ClaimSearchBatch(t.Context(), test.limit, time.Minute)
				require.NoError(t, err)
				live, history := countSearchClasses(t, store, work)
				assert.Equal(t, test.wantLive, live)
				assert.Equal(t, test.wantHistory, history)
				assert.LessOrEqual(t, len(work), test.limit)
			},
		)
	}
}

func TestSearchPriorityPostgresGivenFutureOrLeasedWorkThenSkipsItAndRejectsUnknownKinds(
	t *testing.T,
) {
	store := searchPriorityStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO search_sync_state(kind,source_id,is_backfill,available_at,lease_until)
 VALUES ('message','future-live',false,NOW()+INTERVAL '1 hour',NULL),
 ('message','future-history',true,NOW()+INTERVAL '1 hour',NULL),
 ('message','leased-live',false,NOW(),NOW()+INTERVAL '1 hour'),
 ('message','leased-history',true,NOW(),NOW()+INTERVAL '1 hour'),
 ('message','ready-live',false,NOW(),NULL),('message','ready-history',true,NOW(),NULL)`,
	)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `SELECT pax_search_mark_dirty('unknown','invalid')`)
	require.Error(t, err)
	work, err := store.ClaimSearchBatch(t.Context(), 20, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 2)
	for _, item := range work {
		assert.Contains(t, []string{"ready-live", "ready-history"}, item.Document.SourceID)
	}
	again, err := store.ClaimSearchBatch(t.Context(), 20, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again)
}

func TestSearchPriorityPostgresGivenLiveChangeDuringHistoricalLeaseThenOldAckPreservesPromotion(
	t *testing.T,
) {
	store := searchPriorityStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO search_sync_state(kind,source_id,desired_revision)
 VALUES ('message','message',5)`,
	)
	require.NoError(t, err)
	work, err := store.ClaimSearchBatch(t.Context(), 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 1)
	old := work[0]
	var originalLeaseUntil time.Time
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT lease_until FROM search_sync_state
 WHERE kind='message' AND source_id='message'`).Scan(&originalLeaseUntil),
	)
	_, err = store.db.ExecContext(t.Context(), `UPDATE message_parts SET text='bases'
 WHERE message_id='message' AND part_index=1`)
	require.NoError(t, err)
	var background bool
	var desired, indexed int64
	var token string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT is_backfill,desired_revision,indexed_revision,lease_token
 FROM search_sync_state WHERE kind='message' AND source_id='message'`).
			Scan(&background, &desired, &indexed, &token),
	)
	assert.False(t, background)
	assert.EqualValues(t, 6, desired)
	assert.Zero(t, indexed)
	assert.Equal(t, old.LeaseToken, token)
	var promotedLeaseUntil time.Time
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT lease_until FROM search_sync_state
 WHERE kind='message' AND source_id='message'`).Scan(&promotedLeaseUntil),
	)
	assert.Equal(t, originalLeaseUntil, promotedLeaseUntil)
	require.NoError(t, store.CompleteSearchWork(t.Context(), old, nil))
	next, err := store.ClaimSearchBatch(t.Context(), 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.EqualValues(t, 6, next[0].Document.Revision)
	assert.Equal(t, "databases", next[0].Document.Body)
	require.NoError(t, store.CompleteSearchWork(t.Context(), old, nil))
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT is_backfill,desired_revision,indexed_revision,lease_token
 FROM search_sync_state WHERE kind='message' AND source_id='message'`).
			Scan(&background, &desired, &indexed, &token),
	)
	assert.False(t, background)
	assert.EqualValues(t, 6, desired)
	assert.EqualValues(t, 5, indexed)
	assert.Equal(t, next[0].LeaseToken, token)
	require.NoError(
		t,
		store.CompleteSearchWork(t.Context(), next[0], fmt.Errorf("temporary backend failure")),
	)
	blocked, err := store.ClaimSearchBatch(t.Context(), 20, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, blocked, "priority does not bypass retry availability")
}

func TestSearchPriorityPostgresGivenBackfillThenKeepsExistingLiveRevisionsAndLeases(t *testing.T) {
	store := searchPriorityStore(t)
	_, err := store.db.ExecContext(t.Context(), `SELECT pax_search_mark_dirty('message','message')`)
	require.NoError(t, err)
	work, err := store.ClaimSearchBatch(t.Context(), 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 1)
	require.NoError(t, store.BackfillSearch(t.Context()))
	var background bool
	var revision int64
	var token string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT is_backfill,desired_revision,lease_token
 FROM search_sync_state WHERE kind='message' AND source_id='message'`).Scan(&background, &revision, &token),
	)
	assert.False(t, background)
	assert.Equal(t, work[0].Document.Revision, revision)
	assert.Equal(t, work[0].LeaseToken, token)
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT is_backfill FROM search_sync_state WHERE kind='session'`).
			Scan(&background),
	)
	assert.True(t, background)
}

func TestSearchPriorityPostgresGivenLegacyQueueThenSchemaClassifiesWithoutStartupDML(t *testing.T) {
	store := searchPriorityStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO search_sync_state(kind,source_id,desired_revision,indexed_revision,
 lease_token,lease_until,attempts,last_error) VALUES ('message','legacy',7,3,'existing-lease',NOW()+INTERVAL '1 hour',2,'old failure');
 ALTER TABLE search_sync_state DROP COLUMN IF EXISTS is_backfill CASCADE;
 CREATE FUNCTION reject_search_queue_startup_dml() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'startup queue DML is forbidden'; END $$;
 CREATE TRIGGER reject_search_queue_startup_dml BEFORE INSERT OR UPDATE OR DELETE ON search_sync_state
 FOR EACH STATEMENT EXECUTE FUNCTION reject_search_queue_startup_dml()`,
	)
	require.NoError(t, err)
	var before string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT to_jsonb(q)::text FROM search_sync_state q`).
			Scan(&before),
	)
	schema, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "search.sql"))
	require.NoError(t, err)
	for range 2 {
		_, err = store.db.ExecContext(t.Context(), string(schema))
		require.NoError(t, err)
		var after string
		var background bool
		require.NoError(
			t,
			store.db.QueryRowContext(t.Context(), `SELECT (to_jsonb(q)-'is_backfill')::text,is_backfill
 FROM search_sync_state q`).
				Scan(&after, &background),
		)
		assert.JSONEq(t, before, after)
		assert.True(t, background)
	}
}

func TestSearchPriorityPostgresGivenExistingLiveQueueThenRepeatedSchemaPreservesEveryField(
	t *testing.T,
) {
	store := searchPriorityStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`SELECT pax_search_mark_dirty('message','live-existing');
 UPDATE search_sync_state SET desired_revision=7,indexed_revision=3,lease_token='existing-lease',
 lease_until=NOW()+INTERVAL '1 hour',attempts=2,last_error='old failure';
 CREATE FUNCTION reject_search_queue_startup_dml() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'startup queue DML is forbidden'; END $$;
 CREATE TRIGGER reject_search_queue_startup_dml BEFORE INSERT OR UPDATE OR DELETE ON search_sync_state
 FOR EACH STATEMENT EXECUTE FUNCTION reject_search_queue_startup_dml()`,
	)
	require.NoError(t, err)
	var before string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT to_jsonb(q)::text FROM search_sync_state q`).
			Scan(&before),
	)
	schema, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "search.sql"))
	require.NoError(t, err)
	for range 2 {
		_, err = store.db.ExecContext(t.Context(), string(schema))
		require.NoError(t, err)
		var after string
		var background bool
		require.NoError(
			t,
			store.db.QueryRowContext(t.Context(), `SELECT to_jsonb(q)::text,is_backfill
 FROM search_sync_state q`).Scan(&after, &background),
		)
		assert.JSONEq(t, before, after)
		assert.False(t, background)
	}
}

func TestSearchPriorityPostgresGivenLargeQueueThenBothLanesUseOrderedIndexWithoutSort(
	t *testing.T,
) {
	store := searchPriorityStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO search_sync_state(kind,source_id,is_backfill)
 SELECT 'message','plan-'||i,i%2=0 FROM generate_series(1,10000) i;
 ANALYZE search_sync_state`,
	)
	require.NoError(t, err)
	var plan string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `EXPLAIN (FORMAT JSON) `+searchClaimSQL,
			20, "explain-only", 60000, 16).Scan(&plan),
	)
	assert.Equal(
		t,
		3,
		strings.Count(plan, `"Index Name": "idx_search_sync_pending_priority"`),
		plan,
	)
	assert.NotContains(t, plan, `"Node Type": "Sort"`, "claim must not sort the pending backlog")
}

func TestSearchPriorityPostgresGivenConcurrentLockedHeadThenSkipsItAndFillsBatch(t *testing.T) {
	store := searchPriorityStore(t)
	seedSearchLive(t, store, 0, 5)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(
		t.Context(),
		`SELECT 1 FROM search_sync_state WHERE source_id='live-0' FOR UPDATE`,
	)
	require.NoError(t, err)
	work, err := store.ClaimSearchBatch(t.Context(), 3, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 3)
	for _, item := range work {
		assert.NotEqual(t, "live-0", item.Document.SourceID)
	}
}

func TestSearchPriorityPostgresGivenNewSourceRowsThenTriggersCreateLiveRevisionOne(t *testing.T) {
	store := searchPriorityStore(t)
	seedSearchHistory(t, store, 100)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO messages
 (message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 VALUES ('new-live-message','agent','session','test','inbound','user','user_message',2);
 INSERT INTO agent_sessions(agent_id,session_id,session_name,node_id)
 VALUES ('agent','new-live-session','New title','node')`)
	require.NoError(t, err)
	work, err := store.ClaimSearchBatch(t.Context(), 3, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 3)
	live, history := countSearchClasses(t, store, work)
	assert.Equal(t, 2, live)
	assert.Equal(t, 1, history)
	for _, item := range work {
		if item.Document.Kind == "session" {
			assert.Equal(t, "New title", item.Document.Title)
			assert.EqualValues(t, 1, item.Document.Revision)
		}
		if item.Document.SourceID == "new-live-message" {
			assert.EqualValues(t, 1, item.Document.Revision)
		}
	}
}

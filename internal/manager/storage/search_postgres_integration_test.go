package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func searchIntegrationStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("PAX_MANAGER_SEARCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_SEARCH_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("search_mvp_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	})
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"init.sql", "summaries.sql", "search.sql"} {
		contents, readErr := os.ReadFile(filepath.Join("..", "..", "..", "db", name))
		require.NoError(t, readErr)
		_, err = db.ExecContext(t.Context(), string(contents))
		require.NoError(t, err, "schema %s", name)
	}
	_, err = db.ExecContext(t.Context(), `
 INSERT INTO users(user_id,email) VALUES ('owner','search@example.test'),('other','other@example.test');
 INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash) VALUES ('node','owner','host','node-key');
 INSERT INTO agents(agent_id,owner_user_id,node_id,hostname,os,api_key_hash) VALUES ('agent','owner','node','host','linux','agent-key');
 INSERT INTO agent_sessions(agent_id,session_id,session_name,node_id) VALUES ('agent','session','Title','node');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 VALUES ('message','agent','session','test','inbound','user','user_message',1);
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 VALUES ('message',0,'text','data'),('message',1,'text','base');`)
	require.NoError(t, err)
	return &PostgresStore{db: db, now: time.Now}
}

func searchIntegrationWork(t *testing.T, store *PostgresStore, sourceID string) domain.SearchWork {
	t.Helper()
	work, err := store.ClaimSearchBatch(t.Context(), 100, time.Minute)
	require.NoError(t, err)
	var wanted domain.SearchWork
	for _, item := range work {
		if item.Document.SourceID == sourceID && item.Document.Kind == "message" {
			wanted = item
		} else {
			require.NoError(t, store.CompleteSearchWork(t.Context(), item, nil))
		}
	}
	require.NotEmpty(t, wanted.LeaseToken)
	return wanted
}

func TestSearchPostgresGivenMultipartChangesWhenClaimedThenDurableVersionCannotBeLost(
	t *testing.T,
) {
	s := searchIntegrationStore(t)
	doc, err := s.ReadSearchDocument(t.Context(), "owner", "message", "message")
	require.NoError(t, err)
	assert.Equal(t, "database", doc.Body)
	_, err = s.ReadSearchDocument(t.Context(), "other", "message", "message")
	assert.ErrorIs(t, err, ErrNotFound)
	work := searchIntegrationWork(t, s, "message")
	assert.Equal(t, "database", work.Document.Body)
	claimed, err := s.ClaimSearchBatch(t.Context(), 100, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, claimed, "an active lease must prevent a second claim")
	_, err = s.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='bases' WHERE message_id='message' AND part_index=1`,
	)
	require.NoError(t, err)
	require.NoError(t, s.CompleteSearchWork(t.Context(), work, nil))
	var desired, indexed int64
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision,indexed_revision FROM search_sync_state WHERE kind='message' AND source_id='message'`).
			Scan(&desired, &indexed),
	)
	assert.Greater(t, desired, indexed)
	// A fresh store instance sees pending work without any in-memory checkpoint.
	restarted := &PostgresStore{db: s.db, now: time.Now}
	next := searchIntegrationWork(t, restarted, "message")
	assert.Equal(t, "databases", next.Document.Body)
	assert.Greater(t, next.Document.Revision, work.Document.Revision)
	require.NoError(t, s.CompleteSearchWork(t.Context(), next, nil))
	_, err = s.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='message'`)
	require.NoError(t, err)
	removed := searchIntegrationWork(t, s, "message")
	assert.True(t, removed.Document.Deleted)
	assert.Greater(t, removed.Document.Revision, next.Document.Revision)
	require.NoError(t, s.CompleteSearchWork(t.Context(), removed, nil))
}

func TestSearchPostgresGivenRollbackOrHeartbeatThenDoesNotCreateSearchChanges(t *testing.T) {
	s := searchIntegrationStore(t)
	var before int64
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM search_sync_state WHERE kind='message' AND source_id='message'`).
			Scan(&before),
	)
	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='changed' WHERE message_id='message'`,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	var after int64
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM search_sync_state WHERE kind='message' AND source_id='message'`).
			Scan(&after),
	)
	assert.Equal(t, before, after)
	var sessionRevision int64
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM search_sync_state WHERE kind='session'`).
			Scan(&sessionRevision),
	)
	_, err = s.db.ExecContext(
		t.Context(),
		`UPDATE agent_sessions SET updated_at=NOW(),message_count=123`,
	)
	require.NoError(t, err)
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM search_sync_state WHERE kind='session'`).
			Scan(&after),
	)
	assert.Equal(t, sessionRevision, after)
}

func TestSearchPostgresGivenVisibilityChangesThenQueuesAndHidesSources(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"archived session", `UPDATE agent_sessions SET archived_at=NOW()`},
		{"deleted session", `DELETE FROM agent_sessions`},
		{"deleted agent", `UPDATE agents SET deleted_at=NOW()`},
		{"deleted node", `UPDATE nodes SET deleted_at=NOW()`},
		{"changed owner", `UPDATE agents SET owner_user_id='other'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := searchIntegrationStore(t)
			old := searchIntegrationWork(t, s, "message")
			require.NoError(t, s.CompleteSearchWork(t.Context(), old, nil))
			_, err := s.db.ExecContext(t.Context(), tc.mutation)
			require.NoError(t, err)
			_, err = s.ReadSearchDocument(t.Context(), "owner", "message", "message")
			assert.ErrorIs(t, err, ErrNotFound)
			next := searchIntegrationWork(t, s, "message")
			assert.Greater(t, next.Document.Revision, old.Document.Revision)
			if tc.name != "changed owner" {
				assert.True(t, next.Document.Deleted)
			} else {
				assert.Equal(t, "other", next.Document.OwnerUserID)
			}
		})
	}
}

func TestSearchPostgresGivenInvocationDisplayThenHidesAndRetiresReplacedRawMessage(t *testing.T) {
	s := searchIntegrationStore(t)
	old := searchIntegrationWork(t, s, "message")
	require.NoError(t, s.CompleteSearchWork(t.Context(), old, nil))
	_, err := s.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json)
 VALUES ('display','agent','session','test','inbound','assistant','pax:invocation','message','{"replaces_message_ids":["message"]}');
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('display',0,'text','visible result')`,
	)
	require.NoError(t, err)
	_, err = s.ReadSearchDocument(t.Context(), "owner", "message", "message")
	assert.ErrorIs(t, err, ErrNotFound)
	work := searchIntegrationWork(t, s, "message")
	assert.True(t, work.Document.Deleted)
	assert.Greater(t, work.Document.Revision, old.Document.Revision)
	require.NoError(t, s.CompleteSearchWork(t.Context(), work, nil))
	_, err = s.db.ExecContext(
		t.Context(),
		`UPDATE messages SET raw_json='{"replaces_message_ids":[123]}' WHERE message_id='display'`,
	)
	require.NoError(t, err)
	_, err = s.ReadSearchDocument(t.Context(), "owner", "message", "message")
	assert.ErrorIs(t, err, ErrNotFound, "invalid replacements must fall back to the display parent")
	_, err = s.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='display'`)
	require.NoError(t, err)
	work = searchIntegrationWork(t, s, "message")
	assert.Equal(t, "database", work.Document.Body)
}

func TestSearchPostgresGivenExpiredLeaseThenNewTokenProtectsAgainstOldCompletion(t *testing.T) {
	s := searchIntegrationStore(t)
	old := searchIntegrationWork(t, s, "message")
	_, err := s.db.ExecContext(
		t.Context(),
		`UPDATE search_sync_state SET lease_until=NOW()-INTERVAL '1 second' WHERE kind='message'`,
	)
	require.NoError(t, err)
	next := searchIntegrationWork(t, s, "message")
	assert.NotEqual(t, old.LeaseToken, next.LeaseToken)
	require.NoError(t, s.CompleteSearchWork(t.Context(), old, nil))
	var indexed int64
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT indexed_revision FROM search_sync_state WHERE kind='message'`).
			Scan(&indexed),
	)
	assert.Zero(t, indexed)
	require.NoError(t, s.CompleteSearchWork(t.Context(), next, fmt.Errorf("temporary ES error")))
	var retryAt time.Time
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT available_at FROM search_sync_state WHERE kind='message'`).
			Scan(&retryAt),
	)
	assert.True(t, retryAt.After(time.Now()))
	_, err = s.db.ExecContext(
		t.Context(),
		`UPDATE search_sync_state SET available_at=NOW() WHERE kind='message'`,
	)
	require.NoError(t, err)
	recovered := searchIntegrationWork(t, s, "message")
	require.NoError(t, s.CompleteSearchWork(t.Context(), recovered, nil))
}

func TestSearchPostgresGivenHistoricalRowsWhenBackfilledThenQueueIsIdempotent(t *testing.T) {
	s := searchIntegrationStore(t)
	_, err := s.db.ExecContext(t.Context(), `TRUNCATE search_sync_state`)
	require.NoError(t, err)
	require.NoError(t, s.BackfillSearch(t.Context()))
	require.NoError(t, s.BackfillSearch(t.Context()))
	var count int
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM search_sync_state`).Scan(&count),
	)
	assert.Equal(t, 2, count)
	work := searchIntegrationWork(t, s, "message")
	assert.Equal(t, int64(1), work.Document.Revision)
	assert.Equal(t, "database", work.Document.Body)
	for range 2 {
		contents, readErr := os.ReadFile(filepath.Join("..", "..", "..", "db", "search.sql"))
		require.NoError(t, readErr)
		_, err = s.db.ExecContext(t.Context(), string(contents))
		require.NoError(t, err, "migration must be repeatable")
	}
}

func TestSearchPostgresGivenSnapshotDependenciesWhenSourceChangesThenMarksAncestors(t *testing.T) {
	s := searchIntegrationStore(t)
	_, err := s.db.ExecContext(t.Context(), `
 INSERT INTO summary_versions(summary_id,revision,owner_user_id,subject_type,subject_id,body,request_id,request_hash,created_at)
 VALUES ('child',1,'owner','topic','topic','Child','request1','hash1',NOW()),
        ('parent',1,'owner','project','project','Parent','request2','hash2',NOW());
 INSERT INTO message_snapshots(message_id,canonicalization_version,content_hash,owner_user_id,agent_id,message_created_at,body,created_at)
 VALUES ('message','visible-text-v1','hash','owner','agent',NOW(),'database',NOW());
 INSERT INTO summary_sources(summary_id,revision,source_order,source_kind,message_id,canonicalization_version,content_hash)
 VALUES ('child',1,0,'message','message','visible-text-v1','hash');
 INSERT INTO summary_sources(summary_id,revision,source_order,source_kind,child_summary_id,child_revision)
 VALUES ('parent',1,0,'summary','child',1);`)
	require.NoError(t, err)
	var before, after int64
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM search_sync_state WHERE kind='summary' AND source_id='parent'`).
			Scan(&before),
	)
	_, err = s.db.ExecContext(t.Context(), `UPDATE agents SET deleted_at=NOW()`)
	require.NoError(t, err)
	require.NoError(
		t,
		s.db.QueryRowContext(t.Context(), `SELECT desired_revision FROM search_sync_state WHERE kind='summary' AND source_id='parent'`).
			Scan(&after),
	)
	assert.Greater(t, after, before)
}

func TestSearchPostgresGivenContextRequestThenReadsOrderedVisibleNeighbors(t *testing.T) {
	s := searchIntegrationStore(t)
	_, err := s.db.ExecContext(t.Context(), `
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,session_seq)
 VALUES ('middle','agent','session','test','inbound','assistant','assistant_message',2),
        ('last','agent','session','test','inbound','user','user_message',3);
 INSERT INTO message_parts(message_id,part_index,part_type,text)
 VALUES ('middle',0,'text','middle'),('last',0,'text','last');`)
	require.NoError(t, err)
	docs, err := s.ReadSearchContext(t.Context(), "owner", "middle", 1, 1)
	require.NoError(t, err)
	require.Len(t, docs, 3)
	assert.Equal(
		t,
		[]string{"message", "middle", "last"},
		[]string{docs[0].SourceID, docs[1].SourceID, docs[2].SourceID},
	)
}

var _ domain.SearchSourceStore = (*PostgresStore)(nil)
var _ domain.SearchSourceStore = (*MemoryStore)(nil)
var _ domain.SearchQueueStore = (*PostgresStore)(nil)

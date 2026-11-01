package storage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func searchIndexCatalog(
	t *testing.T,
	store *PostgresStore,
	name, table string,
	keys []string,
	predicateParts []string,
) {
	t.Helper()
	var valid, ready bool
	var actualTable, predicate string
	var rawKeys []byte
	err := store.db.QueryRowContext(t.Context(), `SELECT i.indisvalid,i.indisready,t.relname,
 array_to_json(ARRAY(SELECT pg_get_indexdef(i.indexrelid,k,true) FROM generate_series(1,i.indnkeyatts) k)),
 pg_get_expr(i.indpred,i.indrelid)
 FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid JOIN pg_class t ON t.oid=i.indrelid
 JOIN pg_namespace n ON n.oid=idx.relnamespace WHERE n.nspname=current_schema() AND idx.relname=$1`, name).
		Scan(&valid, &ready, &actualTable, &rawKeys, &predicate)
	require.NoError(t, err, "expected durable index %s", name)
	assert.True(t, valid)
	assert.True(t, ready)
	assert.Equal(t, table, actualTable)
	var actualKeys []string
	require.NoError(t, json.Unmarshal(rawKeys, &actualKeys))
	assert.Equal(t, keys, actualKeys)
	for _, part := range predicateParts {
		assert.Contains(t, predicate, part)
	}
}

func TestSearchIndexesPostgresGivenFreshSchemaThenBothPartialIndexesAreValid(t *testing.T) {
	store := searchIntegrationStore(t)
	searchIndexCatalog(
		t,
		store,
		"idx_messages_search_display_agent_session",
		"messages",
		[]string{"agent_id", "session_id"},
		[]string{"message_type", "pax:invocation'", "pax:invocation_pending", "pax:artifact"},
	)
	searchIndexCatalog(
		t,
		store,
		"idx_search_sync_pending_fifo",
		"search_sync_state",
		[]string{"available_at", "kind", "source_id"},
		[]string{"desired_revision > indexed_revision"},
	)
}

func TestSearchIndexesPostgresGivenLargeTiedBacklogThenClaimUsesOrderedIndexWithoutSorting(
	t *testing.T,
) {
	store := searchIntegrationStore(t)
	_, err := store.db.ExecContext(t.Context(), `TRUNCATE search_sync_state;
 INSERT INTO search_sync_state(kind,source_id,available_at)
 SELECT 'message','backfill-'||lpad(i::text,5,'0'),'2000-01-01'::timestamptz FROM generate_series(1,10000) i;
 ANALYZE search_sync_state`)
	require.NoError(t, err)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{columns: []string{"kind", "id", "revision", "token"}}},
	}
	captured, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	_, err = captured.ClaimSearchBatch(t.Context(), 20, time.Minute)
	require.NoError(t, err)
	require.Len(t, script.queryTexts, 1)
	arguments := make([]any, len(script.queryArgs[0]))
	for i, argument := range script.queryArgs[0] {
		arguments[i] = argument.Value
	}
	arguments[1] = "explain-only"
	tx, err := store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var plan string
	require.NoError(
		t,
		tx.QueryRowContext(t.Context(), `EXPLAIN (FORMAT JSON) `+script.queryTexts[0], arguments...).
			Scan(&plan),
	)
	require.NoError(t, tx.Rollback())
	assert.Contains(t, plan, `"Index Name": "idx_search_sync_pending_fifo"`)
	assert.NotContains(t, plan, `"Node Type": "Sort"`)
	assert.NotContains(t, plan, `"Node Type": "Incremental Sort"`)
	work, err := store.ClaimSearchBatch(t.Context(), 20, time.Minute)
	require.NoError(t, err)
	require.Len(t, work, 20)
	ids := make([]string, 0, len(work))
	for _, item := range work {
		ids = append(ids, item.Document.SourceID)
	}
	assert.Contains(t, ids, "backfill-00001")
	assert.Contains(t, ids, "backfill-00020")
	assert.NotContains(t, ids, "backfill-00021")
}

func TestSearchIndexesPostgresGivenManyUnrelatedMessagesThenDisplayLookupUsesPartialIndex(
	t *testing.T,
) {
	store := searchIntegrationStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type)
 SELECT 'unrelated-'||i,'agent','unrelated-session-'||i,'test','inbound','user','user_message' FROM generate_series(1,10000) i;
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json)
 VALUES ('display','agent','different-session','test','inbound','assistant','pax:artifact','different-parent','{}');
 ANALYZE messages`,
	)
	require.NoError(t, err)
	var plan string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `EXPLAIN (FORMAT JSON) SELECT m.message_id`+searchMessageVisibilitySQL, "message").
			Scan(&plan),
	)
	assert.Contains(t, plan, `"Index Name": "idx_messages_search_display_agent_session"`)
	document, err := store.ReadSearchDocument(t.Context(), "owner", "message", "message")
	require.NoError(t, err)
	assert.Equal(t, "database", document.Body)
}

func TestSearchIndexesPostgresGivenExistingTablesThenOnlineFilesBuildAndRefuseSilentRerun(
	t *testing.T,
) {
	store := searchIntegrationStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`DROP INDEX IF EXISTS idx_messages_search_display_agent_session;
 DROP INDEX IF EXISTS idx_search_sync_pending_fifo`,
	)
	require.NoError(t, err)
	for _, file := range []string{"20261101_search_display_index.sql", "20261101_search_pending_fifo_index.sql"} {
		sql, readErr := os.ReadFile(filepath.Join("..", "..", "..", "db", "migrations", file))
		require.NoError(t, readErr)
		_, err = store.db.ExecContext(t.Context(), string(sql))
		require.NoError(t, err, "standalone autocommit build %s", file)
		_, err = store.db.ExecContext(t.Context(), string(sql))
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "a same-name index must require operator verification")
		assert.Equal(t, "42P07", pgErr.Code)
	}
	searchIndexCatalog(
		t,
		store,
		"idx_messages_search_display_agent_session",
		"messages",
		[]string{"agent_id", "session_id"},
		[]string{"pax:invocation'", "pax:invocation_pending", "pax:artifact"},
	)
	searchIndexCatalog(
		t,
		store,
		"idx_search_sync_pending_fifo",
		"search_sync_state",
		[]string{"available_at", "kind", "source_id"},
		[]string{"desired_revision > indexed_revision"},
	)
}

func TestSearchIndexesPostgresGivenDisplayTypesThenReplacementScopeAndFallbackRemainUnchanged(
	t *testing.T,
) {
	store := searchIntegrationStore(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agents(agent_id,owner_user_id,node_id,hostname,os,api_key_hash)
 VALUES ('other-agent','other','node','other','linux','other-key');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,raw_json)
 VALUES ('foreign-agent-display','other-agent','session','test','inbound','assistant','pax:artifact','{"replaces_message_ids":["message"]}'),
 ('foreign-session-display','agent','other-session','test','inbound','assistant','pax:invocation','{"replaces_message_ids":["message"]}');
 INSERT INTO messages(message_id,agent_id,session_id,source,direction,role,message_type,parent_message_id,raw_json)
 VALUES ('display','agent','session','test','inbound','assistant','pax:invocation','message','{"replaces_message_ids":["unrelated"]}');
 INSERT INTO message_parts(message_id,part_index,part_type,text) VALUES ('display',0,'text','visible replacement')`,
	)
	require.NoError(t, err)
	for _, kind := range []string{"pax:invocation", "pax:invocation_pending", "pax:artifact"} {
		for _, test := range []struct {
			name, raw, parent string
			hidden            bool
		}{
			{"explicit target", `{"replaces_message_ids":["message"]}`, "unrelated-parent", true},
			{"explicit other", `{"replaces_message_ids":["unrelated"]}`, "message", false},
			{"invalid list", `{"replaces_message_ids":[123]}`, "message", true},
			{"empty list", `{"replaces_message_ids":[]}`, "message", true},
			{"missing list", `{}`, "message", true},
		} {
			t.Run(strings.TrimPrefix(kind, "pax:")+"/"+test.name, func(t *testing.T) {
				_, updateErr := store.db.ExecContext(
					t.Context(),
					`UPDATE messages SET message_type=$1,raw_json=$2,parent_message_id=$3 WHERE message_id='display'`,
					kind,
					test.raw,
					test.parent,
				)
				require.NoError(t, updateErr)
				document, readErr := store.ReadSearchDocument(
					t.Context(),
					"owner",
					"message",
					"message",
				)
				if test.hidden {
					assert.ErrorIs(t, readErr, ErrNotFound)
				} else {
					require.NoError(t, readErr)
					assert.Equal(t, "database", document.Body)
				}
				display, readErr := store.ReadSearchDocument(
					t.Context(),
					"owner",
					"message",
					"display",
				)
				require.NoError(t, readErr)
				assert.Equal(t, "visible replacement", display.Body)
			})
		}
	}
	_, err = store.db.ExecContext(t.Context(), `DELETE FROM messages WHERE message_id='display'`)
	require.NoError(t, err)
	document, err := store.ReadSearchDocument(t.Context(), "owner", "message", "message")
	require.NoError(t, err)
	assert.Equal(t, "database", document.Body, "other agents and sessions cannot hide this source")
}

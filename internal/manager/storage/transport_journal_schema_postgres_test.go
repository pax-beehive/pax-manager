package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransportJournalSchemaGivenExistingRowsWhenStartupRunsThenPreservesJournalAndWatermarks(
	t *testing.T,
) {
	dsn := os.Getenv("PAX_MANAGER_JOURNAL_SCHEMA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_JOURNAL_SCHEMA_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("journal_startup_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	t.Chdir("../../..")
	store := NewPostgresStore(db, time.Now)
	require.NoError(t, store.EnsureSchema(t.Context()))
	_, err = db.ExecContext(t.Context(), `
        INSERT INTO users(user_id,email) VALUES ('journal-owner','journal-owner@example.test');
        INSERT INTO agents(agent_id,owner_user_id,hostname,os,api_key_hash)
        VALUES ('journal-agent','journal-owner','fixture','linux','journal-fixture-key');
        INSERT INTO transport_journal(queue_id,agent_id,stream,seq,direction,local_direction,
            status,error,error_message,payload_json)
        VALUES ('','journal-agent','manager_to_paxd',11,'','outbound','acked','legacy','', '{}'),
               ('','journal-agent','paxd_to_manager',12,'','inbound','applied','legacy-inbound','', '{}'),
               ('modern-queue','journal-agent','acp',13,'outbound','outbound','sent','old','current', '{}');
        INSERT INTO transport_queue_state(queue_id,stream,next_outbound_seq,
            inbound_acked_through,inbound_applied_through)
        VALUES ('journal-agent','acp',57,23,19),('modern-queue','acp',14,8,7);
    `)
	require.NoError(t, err)
	readSnapshot := func(table, order string) string {
		t.Helper()
		var snapshot string
		require.NoError(t, db.QueryRowContext(t.Context(),
			"SELECT jsonb_agg(to_jsonb(record) ORDER BY "+order+")::text FROM "+table+" record",
		).Scan(&snapshot))
		return snapshot
	}
	journalBefore := readSnapshot("transport_journal", "id")
	watermarksBefore := readSnapshot("transport_queue_state", "queue_id, stream")
	_, err = db.ExecContext(t.Context(), `
        CREATE FUNCTION reject_startup_journal_writes() RETURNS TRIGGER LANGUAGE plpgsql AS $$
        BEGIN
            RAISE EXCEPTION 'startup must not issue journal DML';
        END $$;
        CREATE TRIGGER reject_startup_journal_writes
        BEFORE INSERT OR UPDATE OR DELETE ON transport_journal
        FOR EACH STATEMENT EXECUTE FUNCTION reject_startup_journal_writes();
    `)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE transport_journal SET status=status WHERE FALSE`)
	require.ErrorContains(t, err, "startup must not issue journal DML",
		"the statement guard must also reject writes that match no rows")
	for range 2 {
		require.NoError(
			t,
			store.EnsureSchema(t.Context()),
			"startup schema application must remain repeatable",
		)
		assert.JSONEq(t, journalBefore, readSnapshot("transport_journal", "id"),
			"startup must not rewrite existing queue IDs, directions, errors, or legacy streams")
		assert.JSONEq(
			t,
			watermarksBefore,
			readSnapshot("transport_queue_state", "queue_id, stream"),
			"startup must not synthesize or recompute queue watermarks from journal history",
		)
	}
}

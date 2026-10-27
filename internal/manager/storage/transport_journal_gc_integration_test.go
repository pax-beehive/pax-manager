//go:build integration

package storage

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGivenCompletedJournalThenPruneOnlyOldRowsBeyondReplayTail(t *testing.T) {
	dsn := os.Getenv("PAX_JOURNAL_GC_TEST_DSN")
	if dsn == "" {
		t.Skip("set PAX_JOURNAL_GC_TEST_DSN for an isolated local PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Equal(t, "/journal_gc_test", u.Path)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, `
		CREATE TABLE transport_queue_state (
			queue_id TEXT, stream TEXT, next_outbound_seq BIGINT,
			inbound_acked_through BIGINT, inbound_applied_through BIGINT,
			PRIMARY KEY(queue_id,stream)
		);
		CREATE TABLE transport_journal (
			id BIGSERIAL PRIMARY KEY, queue_id TEXT, stream TEXT, seq BIGINT,
			direction TEXT, status TEXT, updated_at TIMESTAMPTZ
		);
		INSERT INTO transport_queue_state VALUES ('q','acp',131,130,125),('gap','acp',131,10,130);
		INSERT INTO transport_journal(queue_id,stream,seq,direction,status,updated_at) VALUES
		('q','acp',1,'outbound','acked',NOW()-INTERVAL '4 days'),
		('q','acp',30,'outbound','acked',NOW()-INTERVAL '4 days'),
		('q','acp',31,'outbound','acked',NOW()-INTERVAL '4 days'),
		('q','acp',2,'outbound','acked',NOW()),
		('q','acp',3,'outbound','sent',NOW()-INTERVAL '4 days'),
		('q','acp',1,'inbound','applied',NOW()-INTERVAL '4 days'),
		('q','acp',25,'inbound','applied',NOW()-INTERVAL '4 days'),
		('q','acp',26,'inbound','applied',NOW()-INTERVAL '4 days'),
		('q','acp',4,'inbound','received',NOW()-INTERVAL '4 days'),
		('q','acp',5,'inbound','rejected',NOW()-INTERVAL '4 days'),
		('q','acp',6,'inbound','failed',NOW()-INTERVAL '4 days'),
		('q','acp',7,'inbound','applied',NOW()),
		('gap','acp',1,'inbound','applied',NOW()-INTERVAL '4 days'),
		('missing','acp',1,'outbound','acked',NOW()-INTERVAL '4 days');
	`)
	require.NoError(t, err)
	store := &PostgresStore{db: db}
	cutoff := time.Now().Add(-72 * time.Hour)
	// A competing cleanup must skip a row another transaction is using.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "SELECT id FROM transport_journal WHERE id=1 FOR UPDATE")
	require.NoError(t, err)
	deleted, err := store.PruneCompletedTransportFrames(ctx, cutoff, 1000, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
	require.NoError(t, tx.Rollback())
	deleted, err = store.PruneCompletedTransportFrames(ctx, cutoff, 1, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	deleted, err = store.PruneCompletedTransportFrames(ctx, cutoff, 1000, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	var rows, next, acked, applied int64
	require.NoError(
		t,
		db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transport_journal").Scan(&rows),
	)
	assert.Equal(t, int64(10), rows)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT next_outbound_seq,inbound_acked_through,inbound_applied_through
		FROM transport_queue_state WHERE queue_id='q'
	`).Scan(&next, &acked, &applied))
	assert.Equal(t, int64(131), next)
	assert.Equal(t, int64(130), acked)
	assert.Equal(t, int64(125), applied)
}

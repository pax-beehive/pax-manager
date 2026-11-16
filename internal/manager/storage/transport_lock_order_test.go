package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/require"
)

func TestGivenConcurrentReceiveAndApplyThenTransportLocksDoNotDeadlock(t *testing.T) {
	for _, mode := range []string{"apply", "reject", "batch_frame", "batch_patch"} {
		t.Run(mode, func(t *testing.T) { testConcurrentTransportReceiveAndApply(t, mode) })
	}
}

func testConcurrentTransportReceiveAndApply(t *testing.T, mode string) {
	dsn := os.Getenv("PAX_MANAGER_LOCK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_LOCK_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("transport_locks_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(
		func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") },
	)
	cfg.RuntimeParams["search_path"] = schema
	open := func() *sql.DB {
		db := stdlib.OpenDB(*cfg)
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	gate, applyDB, receiveDB := open(), open(), open()
	_, err = gate.ExecContext(ctx, transportLockFixtureSQL)
	require.NoError(t, err)
	var gatePID, applyPID, receivePID int
	require.NoError(t, gate.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&gatePID))
	require.NoError(t, applyDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&applyPID))
	require.NoError(t, receiveDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&receivePID))
	_, err = gate.ExecContext(ctx, "SELECT pg_advisory_lock(91827365)")
	require.NoError(t, err)
	defer func() { _, _ = gate.ExecContext(context.Background(), "SELECT pg_advisory_unlock(91827365)") }()
	done := make(chan error, 2)
	key := reliablemq.FrameKey{
		QueueID:   "q",
		Stream:    reliablemq.StreamACP,
		Seq:       1,
		Direction: reliablemq.DirectionInbound,
	}
	go func() {
		_, _, receiveErr := NewPostgresStore(
			receiveDB,
			time.Now,
		).SaveInboundIfAbsent(ctx, reliablemq.Frame{
			Key: key, Kind: reliablemq.FrameKindData, Payload: []byte(`{"method":"session/update"}`),
		})
		done <- receiveErr
	}()
	waitForPostgresBlocker(t, ctx, admin, receivePID, gatePID)
	go func() {
		store := NewPostgresStore(applyDB, time.Now)
		switch mode {
		case "reject":
			done <- store.MarkRejected(ctx, key, "rejected")
		case "batch_frame":
			done <- store.ApplyBatch(ctx, reliablemq.StoreBatch{Frames: []reliablemq.Frame{{
				Key: key, Kind: reliablemq.FrameKindData, Payload: []byte(`{}`), Status: reliablemq.StatusApplied,
			}}})
		case "batch_patch":
			done <- store.ApplyBatch(ctx, reliablemq.StoreBatch{Patches: []reliablemq.StorePatch{{
				Key: key, Status: reliablemq.StatusApplied,
			}}})
		default:
			done <- store.MarkApplied(ctx, key)
		}
	}()
	waitForPostgresBlocker(t, ctx, admin, applyPID, receivePID)
	_, err = gate.ExecContext(ctx, "SELECT pg_advisory_unlock(91827365)")
	require.NoError(t, err)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	var through int64
	require.NoError(
		t,
		gate.QueryRowContext(ctx, "SELECT inbound_applied_through FROM transport_queue_state WHERE queue_id='q'").
			Scan(&through),
	)
	require.Equal(t, int64(1), through)
}

const transportLockFixtureSQL = `
CREATE TABLE transport_queue_state(queue_id text,stream text,next_outbound_seq bigint,inbound_acked_through bigint,
 inbound_applied_through bigint,created_at timestamptz,updated_at timestamptz,PRIMARY KEY(queue_id,stream));
CREATE TABLE transport_journal(id bigserial,queue_id text,agent_id text,stream text,seq bigint,direction text,
 local_direction text,kind text,payload_json jsonb,metadata_json jsonb,status text,error_message text,error text,
 retry_count integer,sent_at timestamptz,acked_at timestamptz,
 applied_at timestamptz,received_at timestamptz,created_at timestamptz,updated_at timestamptz,
 UNIQUE(queue_id,stream,seq,direction));
INSERT INTO transport_queue_state VALUES('q','acp',1,0,0,now(),now());
INSERT INTO transport_journal(queue_id,agent_id,stream,seq,direction,kind,payload_json,metadata_json,status,created_at,updated_at)
 VALUES('q','agent','acp',1,'inbound','data','{"method":"session/update"}','{}','received',now(),now());
CREATE FUNCTION gate_transport_receive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(91827365);
 RETURN NEW;
END $$;
CREATE TRIGGER transport_receive_gate BEFORE INSERT ON transport_journal
 FOR EACH ROW EXECUTE FUNCTION gate_transport_receive();`

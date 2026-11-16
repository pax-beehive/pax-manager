package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGivenStaleReceivedFramesThenMemoryReplayStartsAfterAppliedWatermark(t *testing.T) {
	store := NewMemoryStore(time.Now)
	ctx := t.Context()
	for seq := int64(1); seq <= 1011; seq++ {
		frame := testTransportFrame("q", "acp", seq, "inbound", "received")
		require.NoError(t, store.SaveTransportFrame(ctx, &frame))
	}
	store.mu.Lock()
	store.transportQueueState[transportQueueStateKey{QueueID: "q", Stream: "acp"}] = transportQueueState{
		InboundAppliedThrough: 1010,
	}
	store.mu.Unlock()
	frames, err := store.ListInboundReplay(ctx, "q", reliablemq.StreamACP, 1000)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Equal(t, int64(1011), frames[0].Key.Seq)
	// Replay filtering preserves the original failed records for diagnosis.
	frame, err := store.GetTransportFrame(ctx, "q", "acp", 1, "inbound")
	require.NoError(t, err)
	assert.Equal(t, "received", frame.Status)
}

func TestGivenStaleReceivedFramesThenPostgresReplayStartsAfterAppliedWatermark(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_LOCK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_LOCK_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("replay_watermark_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(
		func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") },
	)
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `
CREATE TABLE transport_queue_state(queue_id text,stream text,inbound_applied_through bigint,PRIMARY KEY(queue_id,stream));
CREATE TABLE transport_journal(id bigserial,queue_id text,agent_id text DEFAULT '',stream text,seq bigint,direction text,
 kind text,payload_json jsonb,metadata_json jsonb,status text,error_message text,error text,created_at timestamptz,updated_at timestamptz);
INSERT INTO transport_queue_state VALUES('q','acp',1010),('q','other',0);
INSERT INTO transport_journal(queue_id,stream,seq,direction,kind,status,created_at,updated_at)
 SELECT 'q','acp',n,'inbound','data','received',now(),now() FROM generate_series(1,1011) n;
INSERT INTO transport_journal(queue_id,stream,seq,direction,kind,status,created_at,updated_at)
 VALUES('q','acp',1,'outbound','data','pending',now(),now()),
 ('no_state','acp',1,'inbound','data','received',now(),now()),
 ('q','other',1,'inbound','data','received',now(),now());`)
	require.NoError(t, err)
	store := NewPostgresStore(db, time.Now)
	frames, err := store.ListInboundReplay(ctx, "q", reliablemq.StreamACP, 1000)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Equal(t, int64(1011), frames[0].Key.Seq)
	frames, err = store.ListOutboundReplay(ctx, "q", reliablemq.StreamACP, 1000)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Equal(t, int64(1), frames[0].Key.Seq)
	for _, key := range [][2]string{{"no_state", "acp"}, {"q", "other"}} {
		frames, err = store.ListInboundReplay(ctx, key[0], reliablemq.Stream(key[1]), 1000)
		require.NoError(t, err)
		require.Len(t, frames, 1)
	}
	var status string
	require.NoError(
		t,
		db.QueryRowContext(ctx, "SELECT status FROM transport_journal WHERE queue_id='q' AND stream='acp' AND direction='inbound' AND seq=1").
			Scan(&status),
	)
	assert.Equal(t, "received", status)
}

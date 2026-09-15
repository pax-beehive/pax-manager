package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Use only an isolated test database. Each case owns and removes its schema.
func TestPostgresSnapshotDoesNotDeadlockMessageForeignKey(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_LOCK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_LOCK_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	for _, active := range []bool{false, true} {
		t.Run(
			fmt.Sprintf("active_%t", active),
			func(t *testing.T) { testSnapshotMessageLocks(t, dsn, active) },
		)
	}
}

func testSnapshotMessageLocks(t *testing.T, dsn string, active bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	defer func() { _ = admin.Close() }()
	schema := fmt.Sprintf("runtime_locks_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	config.RuntimeParams["search_path"] = schema
	open := func() *sql.DB { db := stdlib.OpenDB(*config); db.SetMaxOpenConns(1); return db }
	messageDB, snapshotDB, gate := open(), open(), open()
	defer func() { _ = messageDB.Close(); _ = snapshotDB.Close(); _ = gate.Close() }()
	_, err = gate.ExecContext(ctx, runtimeLockFixtureSQL)
	require.NoError(t, err)
	var messagePID, snapshotPID, gatePID int
	require.NoError(t, messageDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&messagePID))
	require.NoError(
		t,
		snapshotDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&snapshotPID),
	)
	require.NoError(t, gate.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&gatePID))
	_, err = gate.ExecContext(ctx, "SELECT pg_advisory_lock(91827364)")
	require.NoError(t, err)
	defer func() { _, _ = gate.ExecContext(context.Background(), "SELECT pg_advisory_unlock(91827364)") }()
	messageDone, snapshotDone := make(chan error, 1), make(chan error, 1)
	go func() {
		message := Message{
			MessageID:      "msg_1",
			AgentID:        "agent_1",
			SessionID:      "sess_1",
			ConversationID: "conv_1",
			Source:         domain.MessageSourceACPTunnel,
			Direction:      domain.MessageDirectionUserToAgent,
			Role:           "user",
		}
		messageDone <- NewPostgresStore(messageDB, time.Now).UpsertMessage(ctx, &message)
	}()
	waitForPostgresBlocker(t, ctx, admin, messagePID, gatePID)
	snapshot := domain.AgentRuntimeSnapshot{
		AgentID:         "agent_1",
		ConnectionFence: "fence_1",
		Sequence:        1,
		GeneratedAt:     time.Now(),
	}
	if active {
		snapshot.ActiveTurns = []domain.ActiveTurnSnapshot{
			{
				NativeSessionID: "native_1",
				TurnInstanceID:  "turn_1",
				PromptRequestID: json.RawMessage(`1`),
				RuntimeStatus:   domain.RuntimeStatusRunning,
			},
		}
	}
	go func() {
		_, err := NewPostgresStore(
			snapshotDB,
			time.Now,
		).ReplaceAgentActiveTurns(ctx, Node{NodeID: "node_1", OwnerUserID: "user_1"}, snapshot)
		snapshotDone <- err
	}()
	// The snapshot now holds its agent mutex and waits on the message's session.
	waitForPostgresBlocker(t, ctx, admin, snapshotPID, messagePID)
	// Release the message to perform its actual FK check. FOR UPDATE on agents
	// causes 40P01 here; FOR NO KEY UPDATE lets both real store calls commit.
	_, err = gate.ExecContext(ctx, "SELECT pg_advisory_unlock(91827364)")
	require.NoError(t, err)
	require.NoError(t, <-messageDone)
	require.NoError(t, <-snapshotDone)
	var count int
	require.NoError(t, gate.QueryRowContext(ctx, "SELECT count(*) FROM messages").Scan(&count))
	require.Equal(t, 1, count)
}

func waitForPostgresBlocker(t *testing.T, ctx context.Context, db *sql.DB, waiter, blocker int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked bool
		err := db.QueryRowContext(ctx, "SELECT $1 = ANY(pg_blocking_pids($2))", blocker, waiter).
			Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 10*time.Millisecond, "expected deterministic transaction ordering")
}

const runtimeLockFixtureSQL = `
CREATE TABLE agents(agent_id text PRIMARY KEY,node_id text,owner_user_id text,deleted_at timestamptz);
CREATE TABLE node_runtime_fences(node_id text PRIMARY KEY,connection_fence text);
CREATE TABLE agent_runtime_snapshot_heads(agent_id text PRIMARY KEY REFERENCES agents,connection_fence text,last_sequence bigint,runtime_authority text,updated_at timestamptz);
CREATE TABLE agent_sessions(id bigserial PRIMARY KEY,agent_id text REFERENCES agents,session_id text,native_id text,runtime_status text,runtime_turn_instance_id text,run_status text,metadata jsonb,updated_at timestamptz,last_user_message_at timestamptz);
CREATE TABLE agent_native_session_bindings(agent_id text REFERENCES agents,native_session_id text,session_id text,created_at timestamptz,PRIMARY KEY(agent_id,native_session_id));
CREATE TABLE messages(id bigserial PRIMARY KEY,message_id text UNIQUE,conversation_id text,owner_user_id text,node_id text,agent_id text REFERENCES agents,session_id text,source text,direction text,role text,status text,message_type text,parent_message_id text,turn_id text,response_id text,logical_key text,raw_json jsonb,session_seq bigint,conversation_seq bigint,created_at timestamptz,updated_at timestamptz);
INSERT INTO agents VALUES('agent_1','node_1','user_1',NULL);
INSERT INTO node_runtime_fences VALUES('node_1','fence_1');
INSERT INTO agent_runtime_snapshot_heads VALUES('agent_1',NULL,NULL,'frames',now());
INSERT INTO agent_sessions(agent_id,session_id,native_id,runtime_status,metadata,updated_at) VALUES('agent_1','sess_1','native_1','running','{}',now());
CREATE FUNCTION hold_message_before_fk() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM 1 FROM agent_sessions WHERE session_id=NEW.session_id FOR UPDATE;
 PERFORM pg_advisory_xact_lock(91827364);
 RETURN NEW;
END $$;
CREATE TRIGGER hold_message BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION hold_message_before_fk();
`

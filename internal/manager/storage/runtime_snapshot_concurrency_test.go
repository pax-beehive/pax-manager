package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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

func runtimeConcurrencyFixture(t *testing.T) (*sql.DB, func() *sql.DB) {
	t.Helper()
	dsn := os.Getenv("PAX_MANAGER_LOCK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_LOCK_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	schema := fmt.Sprintf("snapshot_concurrency_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, dropErr := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, dropErr)
	})
	config.RuntimeParams["search_path"] = schema
	open := func() *sql.DB {
		db := stdlib.OpenDB(*config)
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		return db
	}
	db := open()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(raw))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `
		INSERT INTO users(user_id,email) VALUES ('owner','runtime-locks@example.test');
		INSERT INTO nodes(node_id,owner_user_id,hostname,api_key_hash)
		VALUES ('node','owner','fixture','node-key');
		INSERT INTO agents(agent_id,node_id,owner_user_id,hostname,os,api_key_hash)
		VALUES ('agent','node','owner','fixture','linux','agent-key'),
		       ('other','node','owner','fixture','linux','other-key');
		INSERT INTO node_runtime_fences(node_id,connection_fence) VALUES ('node','fence');
		INSERT INTO agent_sessions(agent_id,session_id,native_id,runtime_status)
		VALUES ('agent','sess_a','native_a','running'),
		       ('agent','sess_b','native_b','running'),
		       ('agent','sess_idle','native_idle','idle');
	`)
	require.NoError(t, err)
	return db, open
}

func runtimeConcurrencySnapshot(agentID string) domain.AgentRuntimeSnapshot {
	return domain.AgentRuntimeSnapshot{
		AgentID: agentID, ConnectionFence: "fence", Sequence: 1, GeneratedAt: time.Now(),
	}
}

func TestPostgresSnapshotGivenLockedIdleHistoryThenCompletesWithoutWaiting(t *testing.T) {
	db, open := runtimeConcurrencyFixture(t)
	gate, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = gate.Rollback() }()
	_, err = gate.ExecContext(
		t.Context(),
		`SELECT 1 FROM agent_sessions WHERE session_id='sess_idle' FOR UPDATE`,
	)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := NewPostgresStore(open(), time.Now).ReplaceAgentActiveTurns(
		ctx, Node{NodeID: "node", OwnerUserID: "owner"}, runtimeConcurrencySnapshot("agent"),
	)
	require.NoError(t, err, "idle history must not participate in active-turn reconciliation")
	assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
	assert.Len(t, result.Changes, 2)
}

func TestPostgresSnapshotGivenLockedCurrentIdleTurnThenWaitsForThatSession(t *testing.T) {
	db, open := runtimeConcurrencyFixture(t)
	gate, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = gate.Rollback() }()
	_, err = gate.ExecContext(
		t.Context(),
		`SELECT 1 FROM agent_sessions WHERE session_id='sess_idle' FOR UPDATE`,
	)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	snapshotDB := open()
	var snapshotPID, gatePID int
	require.NoError(
		t,
		snapshotDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&snapshotPID),
	)
	require.NoError(t, gate.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&gatePID))
	snapshot := runtimeConcurrencySnapshot("agent")
	snapshot.ActiveTurns = []domain.ActiveTurnSnapshot{{
		NativeSessionID: "native_idle", TurnInstanceID: "turn", PromptRequestID: json.RawMessage(`1`),
		RuntimeStatus: domain.RuntimeStatusRunning,
	}}
	done := make(chan error, 1)
	go func() {
		_, replaceErr := NewPostgresStore(snapshotDB, time.Now).ReplaceAgentActiveTurns(
			ctx, Node{NodeID: "node", OwnerUserID: "owner"}, snapshot,
		)
		done <- replaceErr
	}()
	waitForPostgresBlocker(t, ctx, open(), snapshotPID, gatePID)
	require.NoError(t, gate.Rollback())
	require.NoError(t, <-done)
	var status string
	require.NoError(
		t,
		db.QueryRowContext(ctx, `SELECT runtime_status FROM agent_sessions WHERE session_id='sess_idle'`).
			Scan(&status),
	)
	assert.Equal(t, domain.RuntimeStatusRunning, status)
}

func TestPostgresSnapshotGivenAnotherAgentBlockedThenSharesNodeFence(t *testing.T) {
	db, open := runtimeConcurrencyFixture(t)
	gate, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = gate.Rollback() }()
	_, err = gate.ExecContext(
		t.Context(),
		`SELECT 1 FROM agent_sessions WHERE session_id='sess_a' FOR UPDATE`,
	)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	snapshotDB := open()
	var snapshotPID, gatePID int
	require.NoError(
		t,
		snapshotDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&snapshotPID),
	)
	require.NoError(t, gate.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&gatePID))
	done := make(chan error, 1)
	go func() {
		_, replaceErr := NewPostgresStore(snapshotDB, time.Now).ReplaceAgentActiveTurns(
			ctx, Node{NodeID: "node", OwnerUserID: "owner"}, runtimeConcurrencySnapshot("agent"),
		)
		done <- replaceErr
	}()
	waitForPostgresBlocker(t, ctx, open(), snapshotPID, gatePID)
	otherCtx, otherCancel := context.WithTimeout(ctx, time.Second)
	defer otherCancel()
	_, err = NewPostgresStore(open(), time.Now).ReplaceAgentActiveTurns(
		otherCtx, Node{NodeID: "node", OwnerUserID: "owner"}, runtimeConcurrencySnapshot("other"),
	)
	require.NoError(t, err, "independent agents must not serialize on a read-only node fence")
	rotationDB := open()
	var rotationPID int
	require.NoError(
		t,
		rotationDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&rotationPID),
	)
	rotated := make(chan error, 1)
	go func() {
		rotated <- NewPostgresStore(rotationDB, time.Now).ActivateNodeRuntimeFence(
			ctx, Node{NodeID: "node", OwnerUserID: "owner"}, "new_fence",
		)
	}()
	waitForPostgresBlocker(t, ctx, open(), rotationPID, snapshotPID)
	require.NoError(t, gate.Rollback())
	require.NoError(t, <-done)
	require.NoError(t, <-rotated)
	result, err := NewPostgresStore(open(), time.Now).ReplaceAgentActiveTurns(
		ctx, Node{NodeID: "node", OwnerUserID: "owner"}, runtimeConcurrencySnapshot("other"),
	)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeSnapshotFenced, result.Status)
}

func TestPostgresSnapshotGivenOppositeBatchOrderThenDoesNotDeadlock(t *testing.T) {
	db, open := runtimeConcurrencyFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, `
		CREATE FUNCTION hold_batch_first_session() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		 IF NEW.session_id='sess_b' THEN PERFORM pg_advisory_xact_lock(27182818); END IF;
		 RETURN NEW;
		END $$;
		CREATE TRIGGER hold_batch BEFORE UPDATE ON agent_sessions
		FOR EACH ROW EXECUTE FUNCTION hold_batch_first_session();
		SELECT pg_advisory_lock(27182818);
	`)
	require.NoError(t, err)
	defer func() { _, _ = db.ExecContext(context.Background(), "SELECT pg_advisory_unlock(27182818)") }()
	batchDB, snapshotDB, observer := open(), open(), open()
	var batchPID, snapshotPID, gatePID int
	require.NoError(t, batchDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&batchPID))
	require.NoError(
		t,
		snapshotDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&snapshotPID),
	)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&gatePID))
	batchDone, snapshotDone := make(chan error, 1), make(chan error, 1)
	go func() {
		batchDone <- NewPostgresStore(batchDB, time.Now).UpsertAgentSessions(ctx,
			Node{NodeID: "node", OwnerUserID: "owner"}, "agent", []SessionStatusInput{
				{SessionID: "sess_b", NativeID: "native_b"},
				{SessionID: "sess_a", NativeID: "native_a"},
			})
	}()
	waitForPostgresBlocker(t, ctx, observer, batchPID, gatePID)
	go func() {
		_, replaceErr := NewPostgresStore(snapshotDB, time.Now).ReplaceAgentActiveTurns(
			ctx, Node{NodeID: "node", OwnerUserID: "owner"}, runtimeConcurrencySnapshot("agent"),
		)
		snapshotDone <- replaceErr
	}()
	waitForPostgresBlocker(t, ctx, observer, snapshotPID, batchPID)
	_, err = db.ExecContext(ctx, "SELECT pg_advisory_unlock(27182818)")
	require.NoError(t, err)
	require.NoError(t, <-batchDone)
	require.NoError(t, <-snapshotDone)
}

package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func exerciseTurnQueue(t *testing.T, store domain.TurnQueueStore, agentID, nodeID, ownerID string) {
	t.Helper()
	ctx := t.Context()
	q := domain.QueuedTurn{
		AgentID:   agentID,
		NodeID:    nodeID,
		SessionID: "queue-session",
		OwnerID:   ownerID,
		TurnID:    "turn-queued",
		CommandID: "cmd",
		Input:     "first",
	}
	first, replaced, err := store.PutQueuedTurn(ctx, q, false)
	require.NoError(t, err)
	require.False(t, replaced)
	q.TurnID = "unused-new-id"
	q.Input = "edited"
	q.CommandID = "cmd-edit"
	edited, replaced, err := store.PutQueuedTurn(ctx, q, false)
	require.NoError(t, err)
	require.True(t, replaced)
	require.Equal(t, first.TurnID, edited.TurnID)
	entries, err := store.ListQueuedTurns(ctx, nodeID, agentID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	queued := entries[0]
	require.Equal(t, "edited", queued.Input)
	require.Equal(t, "native-queue", queued.NativeID)
	entries, err = store.ListQueuedTurns(ctx, "another-node", agentID)
	require.NoError(t, err)
	require.Empty(t, entries)
	snapshot := domain.AgentRuntimeSnapshot{
		AgentID:         agentID,
		ConnectionFence: "queue-fence",
		Sequence:        2,
	}
	claimed, err := store.ClaimQueuedTurn(ctx, queued, snapshot)
	require.NoError(t, err)
	require.False(t, claimed, "stale snapshot cannot claim")
	snapshot.Sequence = 3
	stale := queued
	stale.Input = "first"
	claimed, err = store.ClaimQueuedTurn(ctx, stale, snapshot)
	require.NoError(t, err)
	require.False(t, claimed, "an edited draft cannot be sent with old contents")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := store.ClaimQueuedTurn(ctx, queued, snapshot)
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, wins.Load())
	_, _, err = store.PutQueuedTurn(ctx, q, false)
	require.ErrorIs(t, err, domain.ErrConflict)
	_, err = store.DeleteQueuedTurn(ctx, agentID, q.SessionID)
	require.ErrorIs(t, err, domain.ErrConflict)
	require.NoError(t, store.FinishQueuedTurn(ctx, queued, false))
	claimed, err = store.ClaimQueuedTurn(ctx, queued, snapshot)
	require.NoError(t, err)
	require.False(t, claimed, "uncertain delivery must never auto-retry")
	current, err := store.GetQueuedTurn(ctx, agentID, q.SessionID)
	require.NoError(t, err)
	require.Equal(t, "uncertain", current.State)
	_, err = store.DeleteQueuedTurn(ctx, agentID, q.SessionID)
	require.NoError(t, err)
	q.TurnID = "fresh-turn"
	_, _, err = store.PutQueuedTurn(ctx, q, false)
	require.NoError(t, err)
	require.NoError(t, store.FinishQueuedTurn(ctx, queued, true))
	current, err = store.GetQueuedTurn(ctx, agentID, q.SessionID)
	require.NoError(t, err)
	require.Equal(t, "fresh-turn", current.TurnID)
	entries, err = store.ListQueuedTurns(ctx, nodeID, agentID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	claimed, err = store.ClaimQueuedTurn(ctx, entries[0], snapshot)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.FinishQueuedTurn(ctx, entries[0], true))
	_, err = store.GetQueuedTurn(ctx, agentID, q.SessionID)
	require.ErrorIs(t, err, domain.ErrNotFound)
	q.Input = strings.Repeat("x", domain.MaxQueuedTurnBytes+1)
	_, _, err = store.PutQueuedTurn(ctx, q, false)
	require.Error(t, err)
}

func TestMemoryTurnQueueClaimsAndRecovery(t *testing.T) {
	store, node, agent := sessionReportStoreFixture(t, t.Context())
	_, err := store.CreateNodeAgentSession(
		t.Context(),
		UserPrincipal{User: User{UserID: node.OwnerUserID}},
		domain.CreateSessionRequest{
			AgentID: agent.AgentID, NodeID: node.NodeID, SessionID: "queue-session", NativeID: "native-queue",
		},
	)
	require.NoError(t, err)
	require.NoError(t, store.ActivateNodeRuntimeFence(t.Context(), node, "queue-fence"))
	_, err = store.ReplaceAgentActiveTurns(
		t.Context(),
		node,
		runtimeSnapshot(agent.AgentID, "queue-fence", 3),
	)
	require.NoError(t, err)
	exerciseTurnQueue(t, store, agent.AgentID, node.NodeID, node.OwnerUserID)
}

func TestPostgresTurnQueueClaimsAndRecovery(t *testing.T) {
	dsn := os.Getenv("PAX_MANAGER_QUEUE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_QUEUE_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	// All fixtures are connection-local; never mutate persistent database tables.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(ctx, `
 CREATE TEMP TABLE users(user_id text PRIMARY KEY);
 CREATE TEMP TABLE agents(agent_id text PRIMARY KEY,node_id text,deleted_at timestamptz);
 CREATE TEMP TABLE agent_sessions(agent_id text,session_id text,node_id text,native_id text,
 runtime_status text,archived_at timestamptz,UNIQUE(agent_id,session_id));
 CREATE TEMP TABLE agent_runtime_snapshot_heads(agent_id text PRIMARY KEY,connection_fence text,last_sequence bigint);
 CREATE TEMP TABLE node_runtime_fences(node_id text PRIMARY KEY,connection_fence text);
 INSERT INTO users VALUES('owner');
 INSERT INTO agents VALUES('agent','node',NULL);
 INSERT INTO agent_sessions VALUES('agent','queue-session','node','native-queue','idle',NULL);
 INSERT INTO agent_runtime_snapshot_heads VALUES('agent','queue-fence',3);
 INSERT INTO node_runtime_fences VALUES('node','queue-fence');
 `)
	require.NoError(t, err)
	schema, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	start := strings.Index(string(schema), "CREATE TABLE IF NOT EXISTS session_turn_queue")
	require.GreaterOrEqual(t, start, 0)
	ddl := strings.SplitN(string(schema[start:]), ";", 2)[0]
	ddl = strings.Replace(ddl, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE", 1)
	_, err = db.ExecContext(ctx, ddl)
	require.NoError(t, err)
	store := NewPostgresStore(db, time.Now)
	// Reopening the store sees the same pending slot, unlike the old service map.
	_, _, err = store.PutQueuedTurn(
		ctx,
		domain.QueuedTurn{
			AgentID:   "agent",
			SessionID: "queue-session",
			TurnID:    "restart-turn",
			OwnerID:   "owner",
			Input:     "survives restart",
		},
		false,
	)
	require.NoError(t, err)
	reopened := NewPostgresStore(db, time.Now)
	saved, err := reopened.GetQueuedTurn(ctx, "agent", "queue-session")
	require.NoError(t, err)
	require.Equal(t, "survives restart", saved.Input)
	_, err = reopened.DeleteQueuedTurn(ctx, "agent", "queue-session")
	require.NoError(t, err)
	exerciseTurnQueue(t, reopened, "agent", "node", "owner")
}

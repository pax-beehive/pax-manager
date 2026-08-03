package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryRuntimeSnapshotReconciliation(t *testing.T) {
	t.Run("Given a native session when a snapshot arrives then it updates the canonical session", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)
		principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
		session, err := store.CreateNodeAgentSession(ctx, principal, domain.CreateSessionRequest{
			NodeID: node.NodeID, AgentID: agent.AgentID, SessionID: "sess_manager", NativeID: "native_1",
		})
		require.NoError(t, err)
		require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence_1"))

		result, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(
			agent.AgentID,
			"fence_1",
			1,
			domain.ActiveTurnSnapshot{
				NativeSessionID: "native_1", TurnInstanceID: "turn_1",
				PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
			},
		))

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
		assert.True(t, result.AuthorityChanged)
		require.Len(t, result.Changes, 1)
		assert.Equal(t, session.SessionID, result.Changes[0].SessionID)
		stored, err := store.GetSession(ctx, principal, session.SessionID)
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeStatusRunning, stored.RuntimeStatus)
		assert.Equal(t, "turn_1", stored.RuntimeTurnInstanceID)
		assert.Equal(t, domain.RuntimeAuthoritySnapshot, stored.RuntimeAuthority)
	})

	t.Run("Given an unknown native session when a snapshot arrives then it creates one canonical row", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)
		principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
		require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence_1"))

		result, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(
			agent.AgentID,
			"fence_1",
			1,
			domain.ActiveTurnSnapshot{
				NativeSessionID: "native_new", TurnInstanceID: "turn_new",
				PromptRequestID:   json.RawMessage(`"request-1"`),
				RuntimeStatus:     domain.RuntimeStatusWaitingApproval,
				PendingApprovalID: "permission-1",
			},
		))

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
		sessions, err := store.ListAgentSessions(ctx, principal, agent.AgentID)
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		assert.True(t, isManagerSessionID(sessions[0].SessionID))
		assert.Equal(t, "native_new", sessions[0].NativeID)
		assert.Equal(t, domain.RuntimeStatusWaitingApproval, sessions[0].RuntimeStatus)
		assert.Equal(t, "permission-1", sessions[0].RuntimeState.PendingApprovalID)
	})

	t.Run("Given a previous active turn when the next complete snapshot omits it then it becomes idle", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)
		principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
		session, err := store.CreateNodeAgentSession(ctx, principal, domain.CreateSessionRequest{
			NodeID: node.NodeID, AgentID: agent.AgentID, SessionID: "sess_manager", NativeID: "native_1",
		})
		require.NoError(t, err)
		require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence_1"))
		_, err = store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(
			agent.AgentID, "fence_1", 1,
			domain.ActiveTurnSnapshot{
				NativeSessionID: "native_1", TurnInstanceID: "turn_1",
				PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
			},
		))
		require.NoError(t, err)

		result, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(agent.AgentID, "fence_1", 2))

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
		require.Len(t, result.Changes, 1)
		assert.Equal(t, domain.RuntimeStatusIdle, result.Changes[0].RuntimeStatus)
		stored, err := store.GetSession(ctx, principal, session.SessionID)
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeStatusIdle, stored.RuntimeStatus)
		assert.Empty(t, stored.RuntimeTurnInstanceID)
	})

	t.Run("Given duplicate stale and old-fence snapshots then none overwrite the current projection", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)
		principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
		require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence_old"))
		_, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(
			agent.AgentID, "fence_old", 4,
			domain.ActiveTurnSnapshot{
				NativeSessionID: "native_1", TurnInstanceID: "turn_1",
				PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
			},
		))
		require.NoError(t, err)
		duplicate, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(agent.AgentID, "fence_old", 4))
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotDuplicate, duplicate.Status)
		stale, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(agent.AgentID, "fence_old", 3))
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotStale, stale.Status)

		require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence_new"))
		fenced, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(agent.AgentID, "fence_old", 5))
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotFenced, fenced.Status)
		fresh, err := store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(agent.AgentID, "fence_new", 1))
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, fresh.Status)

		sessions, err := store.ListAgentSessions(ctx, principal, agent.AgentID)
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		assert.Equal(t, domain.RuntimeStatusIdle, sessions[0].RuntimeStatus)
	})

	t.Run("Given snapshot authority when inventory and frame projections arrive then canonical runtime stays unchanged", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)
		principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
		session, err := store.CreateNodeAgentSession(ctx, principal, domain.CreateSessionRequest{
			NodeID: node.NodeID, AgentID: agent.AgentID, SessionID: "sess_manager", NativeID: "native_1",
		})
		require.NoError(t, err)
		require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence_1"))
		_, err = store.ReplaceAgentActiveTurns(ctx, node, runtimeSnapshot(
			agent.AgentID, "fence_1", 1,
			domain.ActiveTurnSnapshot{
				NativeSessionID: "native_1", TurnInstanceID: "turn_1",
				PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
			},
		))
		require.NoError(t, err)

		require.NoError(t, store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
			SessionID: "native_1", NativeID: "native_1", Status: "available", RunStatus: "idle",
		}}))
		require.NoError(t, store.UpdateSessionRuntimeState(ctx, domain.SessionRuntimeState{
			AgentID: agent.AgentID, SessionID: session.SessionID,
			Lifecycle: domain.RuntimeLifecycleIdle, UpdatedAt: time.Now().UTC(),
		}))

		stored, err := store.GetSession(ctx, principal, session.SessionID)
		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeStatusRunning, stored.RuntimeStatus)
		assert.Equal(t, "turn_1", stored.RuntimeTurnInstanceID)
	})

	t.Run("Given an agent outside the authenticated node then fence and snapshot writes are rejected", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)
		otherNode, err := store.RegisterNode(ctx, User{UserID: node.OwnerUserID}, domain.RegisterNodeRequest{
			Name: "other",
		}, "node-other-key")
		require.NoError(t, err)

		err = store.ActivateNodeRuntimeFence(ctx, otherNode, "fence_other")
		require.NoError(t, err)
		_, err = store.ReplaceAgentActiveTurns(ctx, otherNode, runtimeSnapshot(agent.AgentID, "fence_other", 1))
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestActiveTurnSnapshotValidation(t *testing.T) {
	tests := []struct {
		name     string
		snapshot domain.AgentRuntimeSnapshot
	}{
		{name: "empty agent", snapshot: runtimeSnapshot("", "fence", 1)},
		{name: "empty fence", snapshot: runtimeSnapshot("agent", "", 1)},
		{name: "zero sequence", snapshot: runtimeSnapshot("agent", "fence", 0)},
		{name: "duplicate native session", snapshot: runtimeSnapshot(
			"agent", "fence", 1,
			domain.ActiveTurnSnapshot{NativeSessionID: "native", TurnInstanceID: "turn_1", PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning},
			domain.ActiveTurnSnapshot{NativeSessionID: "native", TurnInstanceID: "turn_2", PromptRequestID: json.RawMessage(`2`), RuntimeStatus: domain.RuntimeStatusRunning},
		)},
		{name: "duplicate turn", snapshot: runtimeSnapshot(
			"agent", "fence", 1,
			domain.ActiveTurnSnapshot{NativeSessionID: "native_1", TurnInstanceID: "turn", PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning},
			domain.ActiveTurnSnapshot{NativeSessionID: "native_2", TurnInstanceID: "turn", PromptRequestID: json.RawMessage(`2`), RuntimeStatus: domain.RuntimeStatusRunning},
		)},
		{name: "invalid request id", snapshot: runtimeSnapshot(
			"agent", "fence", 1,
			domain.ActiveTurnSnapshot{NativeSessionID: "native", TurnInstanceID: "turn", PromptRequestID: json.RawMessage(`true`), RuntimeStatus: domain.RuntimeStatusRunning},
		)},
		{name: "idle active turn", snapshot: runtimeSnapshot(
			"agent", "fence", 1,
			domain.ActiveTurnSnapshot{NativeSessionID: "native", TurnInstanceID: "turn", PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusIdle},
		)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, test.snapshot.Validate())
		})
	}

	require.NoError(t, runtimeSnapshot(
		"agent", "fence", 1,
		domain.ActiveTurnSnapshot{NativeSessionID: "native_1", TurnInstanceID: "turn_1", PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning},
		domain.ActiveTurnSnapshot{NativeSessionID: "native_2", TurnInstanceID: "turn_2", PromptRequestID: json.RawMessage(`"1"`), RuntimeStatus: domain.RuntimeStatusWaitingApproval},
	).Validate())
}

func TestRuntimeSnapshotSchema(t *testing.T) {
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	sql := string(initSQL)

	assert.Contains(t, sql, "runtime_status TEXT NOT NULL DEFAULT 'idle'")
	assert.Contains(t, sql, "runtime_turn_instance_id TEXT")
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS node_runtime_fences")
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS agent_runtime_snapshot_heads")
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS agent_native_session_bindings")
	assert.Contains(t, sql, "runtime_authority TEXT NOT NULL DEFAULT 'frames'")
	assert.Contains(t, sql, "PRIMARY KEY (agent_id, native_session_id)")
}

func runtimeSnapshot(
	agentID string,
	fence string,
	sequence int64,
	turns ...domain.ActiveTurnSnapshot,
) domain.AgentRuntimeSnapshot {
	return domain.AgentRuntimeSnapshot{
		AgentID: agentID, ConnectionFence: fence, Sequence: sequence,
		GeneratedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC), ActiveTurns: turns,
	}
}

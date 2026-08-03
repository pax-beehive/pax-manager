package storage

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPostgresRuntimeSnapshotReconciliation(t *testing.T) {
	t.Run("Given an authenticated node when activating a fence then the database atomically replaces only that node fence", func(t *testing.T) {
		script := &scriptedPostgresScript{execResults: []int64{1}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		err := store.ActivateNodeRuntimeFence(context.Background(), Node{
			NodeID: "node_1", OwnerUserID: "user_1",
		}, "fence_1")

		require.NoError(t, err)
		require.Len(t, script.execTexts, 1)
		assert.Contains(t, script.execTexts[0], "INSERT INTO node_runtime_fences")
		assert.Contains(t, script.execTexts[0], "FROM nodes")
		assert.Contains(t, script.execTexts[0], "owner_user_id = $2")
		assert.Contains(t, script.execTexts[0], "ON CONFLICT (node_id) DO UPDATE")
	})

	t.Run("Given an invalid fence or missing node when activating then it is rejected", func(t *testing.T) {
		script := &scriptedPostgresScript{execResults: []int64{0}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		require.ErrorIs(t, store.ActivateNodeRuntimeFence(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			" ",
		), ErrConflict)
		require.ErrorIs(t, store.ActivateNodeRuntimeFence(
			context.Background(),
			Node{NodeID: "missing", OwnerUserID: "user_1"},
			"fence_1",
		), ErrNotFound)
	})

	t.Run("Given an old connection fence when replacing turns then it is fenced before agent writes", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{{
			columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_new"}},
		}}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		result, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot("agent_1", "fence_old", 2),
		)

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotFenced, result.Status)
		assert.Empty(t, script.execTexts)
		assert.True(t, script.rolled)
		assert.False(t, script.committed)
	})

	t.Run("Given an agent outside the authenticated node when replacing turns then it is rejected", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
			{columns: []string{"exists"}},
		}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		_, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot("agent_other", "fence_1", 1),
		)

		require.ErrorIs(t, err, ErrNotFound)
		assert.Empty(t, script.execTexts)
		assert.True(t, script.rolled)
		assert.False(t, script.committed)
	})

	t.Run("Given a valid current snapshot then it resolves the binding and commits runtime plus authority", func(t *testing.T) {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
				{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
				{
					columns: []string{"connection_fence", "last_sequence", "runtime_authority"},
					values:  [][]driver.Value{{"fence_1", int64(3), domain.RuntimeAuthorityFrames}},
				},
				{columns: []string{"session_id"}, values: [][]driver.Value{{"sess_manager"}}},
				{columns: []string{"session_id", "native_id", "runtime_status", "runtime_turn_instance_id"}, values: [][]driver.Value{{
					"sess_manager", "native_1", domain.RuntimeStatusIdle, "",
				}}},
			},
			execResults: []int64{1, 1},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		result, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot(
				"agent_1", "fence_1", 4,
				domain.ActiveTurnSnapshot{
					NativeSessionID: "native_1", TurnInstanceID: "turn_1",
					PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
				},
			),
		)

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
		assert.True(t, result.AuthorityChanged)
		require.Len(t, result.Changes, 1)
		assert.Equal(t, "sess_manager", result.Changes[0].SessionID)
		assert.True(t, script.committed)
		require.Len(t, script.execTexts, 2)
		assert.Contains(t, script.execTexts[0], "UPDATE agent_sessions")
		assert.Contains(t, script.execTexts[0], "runtime_status = $3")
		assert.Contains(t, script.execTexts[0], "runtime_turn_instance_id = $4")
		assert.Contains(t, script.execTexts[1], "UPDATE agent_runtime_snapshot_heads")
		assert.Contains(t, script.execTexts[1], "runtime_authority = 'snapshot'")
		assert.Contains(t, strings.Join(script.queryTexts, "\n"), "FOR UPDATE")
	})

	t.Run("Given the same sequence then it is duplicate and does not mutate sessions", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
			{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
			{
				columns: []string{"connection_fence", "last_sequence", "runtime_authority"},
				values:  [][]driver.Value{{"fence_1", int64(4), domain.RuntimeAuthoritySnapshot}},
			},
		}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		result, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot("agent_1", "fence_1", 4),
		)

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotDuplicate, result.Status)
		assert.Empty(t, script.execTexts)
		assert.True(t, script.rolled)
	})

	t.Run("Given a lower sequence on the same fence then it is stale", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
			{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
			{
				columns: []string{"connection_fence", "last_sequence", "runtime_authority"},
				values:  [][]driver.Value{{"fence_1", int64(4), domain.RuntimeAuthoritySnapshot}},
			},
		}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		result, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot("agent_1", "fence_1", 3),
		)

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotStale, result.Status)
		assert.Empty(t, script.execTexts)
		assert.True(t, script.rolled)
	})

	t.Run("Given the first snapshot and an existing native session then it creates the head and idles an absent active session", func(t *testing.T) {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
				{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
				{columns: []string{"connection_fence", "last_sequence", "runtime_authority"}},
				{columns: []string{"session_id"}},
				{columns: []string{"session_id"}, values: [][]driver.Value{{"sess_active"}}},
				{
					columns: []string{"session_id", "native_id", "runtime_status", "runtime_turn_instance_id"},
					values: [][]driver.Value{
						{"sess_active", "native_active", domain.RuntimeStatusIdle, ""},
						{"sess_absent", "native_absent", domain.RuntimeStatusWaitingApproval, "turn_absent"},
					},
				},
			},
			execResults: []int64{1, 1, 1, 1, 1},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		result, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot(
				"agent_1", "fence_1", 1,
				domain.ActiveTurnSnapshot{
					NativeSessionID: "native_active", TurnInstanceID: "turn_active",
					PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
				},
			),
		)

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
		assert.True(t, result.AuthorityChanged)
		assert.ElementsMatch(t, []domain.RuntimeStatusChange{
			{SessionID: "sess_active", NativeSessionID: "native_active", RuntimeStatus: domain.RuntimeStatusRunning, TurnInstanceID: "turn_active"},
			{SessionID: "sess_absent", NativeSessionID: "native_absent", RuntimeStatus: domain.RuntimeStatusIdle},
		}, result.Changes)
		assert.Contains(t, strings.Join(script.execTexts, "\n"), "INSERT INTO agent_runtime_snapshot_heads")
		assert.Contains(t, strings.Join(script.execTexts, "\n"), "runtime_status = 'idle'")
		assert.True(t, script.committed)
	})

	t.Run("Given no canonical native session when a snapshot arrives then it creates and binds a minimal session", func(t *testing.T) {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
				{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
				{
					columns: []string{"connection_fence", "last_sequence", "runtime_authority"},
					values:  [][]driver.Value{{nil, nil, domain.RuntimeAuthorityFrames}},
				},
				{columns: []string{"session_id"}},
				{columns: []string{"session_id"}},
				{columns: []string{"session_id", "native_id", "runtime_status", "runtime_turn_instance_id"}},
			},
			execResults: []int64{1, 1, 1, 1},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		result, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot(
				"agent_1", "fence_1", 1,
				domain.ActiveTurnSnapshot{
					NativeSessionID: "native_new", TurnInstanceID: "turn_new",
					PromptRequestID: json.RawMessage(`"request"`), RuntimeStatus: domain.RuntimeStatusRunning,
				},
			),
		)

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeSnapshotApplied, result.Status)
		assert.Contains(t, strings.Join(script.execTexts, "\n"), "INSERT INTO agent_sessions")
		assert.Contains(t, strings.Join(script.execTexts, "\n"), "INSERT INTO agent_native_session_bindings")
		assert.True(t, script.committed)
	})

	t.Run("Given ambiguous native sessions when a snapshot arrives then the whole transaction is rejected", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
			{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
			{
				columns: []string{"connection_fence", "last_sequence", "runtime_authority"},
				values:  [][]driver.Value{{"fence_1", int64(1), domain.RuntimeAuthorityFrames}},
			},
			{columns: []string{"session_id"}},
			{columns: []string{"session_id"}, values: [][]driver.Value{{"sess_1"}, {"sess_2"}}},
		}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		_, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot(
				"agent_1", "fence_1", 2,
				domain.ActiveTurnSnapshot{
					NativeSessionID: "native_ambiguous", TurnInstanceID: "turn_1",
					PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
				},
			),
		)

		require.ErrorIs(t, err, ErrConflict)
		assert.Empty(t, script.execTexts)
		assert.True(t, script.rolled)
		assert.False(t, script.committed)
	})

	t.Run("Given a conflicting native binding is installed concurrently then the snapshot rolls back", func(t *testing.T) {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{columns: []string{"connection_fence"}, values: [][]driver.Value{{"fence_1"}}},
				{columns: []string{"exists"}, values: [][]driver.Value{{true}}},
				{
					columns: []string{"connection_fence", "last_sequence", "runtime_authority"},
					values:  [][]driver.Value{{"fence_1", int64(1), domain.RuntimeAuthorityFrames}},
				},
				{columns: []string{"session_id"}},
				{columns: []string{"session_id"}, values: [][]driver.Value{{"sess_1"}}},
			},
			execResults: []int64{0},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		_, err := store.ReplaceAgentActiveTurns(
			context.Background(),
			Node{NodeID: "node_1", OwnerUserID: "user_1"},
			runtimeSnapshot(
				"agent_1", "fence_1", 2,
				domain.ActiveTurnSnapshot{
					NativeSessionID: "native_1", TurnInstanceID: "turn_1",
					PromptRequestID: json.RawMessage(`1`), RuntimeStatus: domain.RuntimeStatusRunning,
				},
			),
		)

		require.ErrorIs(t, err, ErrConflict)
		assert.True(t, script.rolled)
		assert.False(t, script.committed)
	})
}

func TestPostgresFrameRuntimeProjectionHonorsSnapshotAuthority(t *testing.T) {
	t.Run("Given snapshot authority when a frame projection arrives then it commits without changing the session", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{columns: []string{"agent_id"}, values: [][]driver.Value{{"agent_1"}}},
			{columns: []string{"runtime_authority"}, values: [][]driver.Value{{domain.RuntimeAuthoritySnapshot}}},
		}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		err := store.UpdateSessionRuntimeState(context.Background(), domain.SessionRuntimeState{
			AgentID: "agent_1", SessionID: "sess_1", Lifecycle: domain.RuntimeLifecycleIdle,
		})

		require.NoError(t, err)
		assert.Empty(t, script.execTexts)
		assert.True(t, script.committed)
		assert.Contains(t, strings.Join(script.queryTexts, "\n"), "FROM agents")
		assert.Contains(t, strings.Join(script.queryTexts, "\n"), "FOR UPDATE")
	})

	t.Run("Given frame authority when a frame projection arrives then it updates canonical runtime columns", func(t *testing.T) {
		script := &scriptedPostgresScript{
			queries: []scriptedRows{
				{columns: []string{"agent_id"}, values: [][]driver.Value{{"agent_1"}}},
				{columns: []string{"runtime_authority"}},
			},
			execResults: []int64{1},
		}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()

		err := store.UpdateSessionRuntimeState(context.Background(), domain.SessionRuntimeState{
			AgentID: "agent_1", SessionID: "sess_1", Lifecycle: domain.RuntimeLifecycleRunning,
			TurnInstanceID: "legacy_turn",
		})

		require.NoError(t, err)
		require.Len(t, script.execTexts, 1)
		assert.Contains(t, script.execTexts[0], "runtime_status")
		assert.Contains(t, script.execTexts[0], "runtime_turn_instance_id")
		assert.True(t, script.committed)
	})
}

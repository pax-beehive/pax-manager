package storage

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreUpsertNodeStatusDeletesReportedAgents(t *testing.T) {
	t.Run(
		"Given a node that removed an agent connection then the agent is deleted for good",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}

			require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
				NodeID:          node.NodeID,
				DeletedAgentIDs: []string{agent.AgentID, "agent_unknown"},
			}))

			_, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)
			deleted, err := store.ListDeletedNodeAgentIDs(
				ctx,
				node.NodeID,
				[]string{agent.AgentID, "agent_unknown"},
			)
			require.NoError(t, err)
			assert.Equal(t, []string{agent.AgentID}, deleted)

			// A later report of the same agent must not bring it back, neither
			// under its old ID nor as a freshly adopted agent.
			require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
				NodeID: node.NodeID,
				Agents: []AgentStatusInput{{
					AgentID:  agent.AgentID,
					Name:     "revived",
					Status:   "online",
					Sessions: []SessionStatusInput{{SessionID: "codex:revived"}},
				}},
			}))
			agents, err := store.ListNodeAgents(ctx, principal, node.NodeID)
			require.NoError(t, err)
			assert.Empty(t, agents)
		},
	)

	t.Run(
		"Given another node reporting the removal then the agent stays",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			otherNode, err := store.RegisterNode(
				ctx,
				User{UserID: node.OwnerUserID},
				RegisterNodeRequest{Name: "node-b", Hostname: "node-b", OS: "linux"},
				"hash_node_b",
			)
			require.NoError(t, err)

			require.NoError(t, store.UpsertNodeStatus(ctx, otherNode, NodeStatusReport{
				NodeID:          otherNode.NodeID,
				DeletedAgentIDs: []string{agent.AgentID},
			}))

			after, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, agent.AgentID, after.AgentID)
			for _, nodeID := range []string{node.NodeID, otherNode.NodeID} {
				deleted, err := store.ListDeletedNodeAgentIDs(ctx, nodeID, []string{agent.AgentID})
				require.NoError(t, err)
				assert.Empty(t, deleted)
			}
		},
	)

	t.Run(
		"Given an agent deleted by its owner then a status report does not revive it",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
			_, err := store.DeleteAgent(ctx, principal, DeleteAgentRequest{AgentID: agent.AgentID})
			require.NoError(t, err)

			require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
				NodeID: node.NodeID,
				Agents: []AgentStatusInput{
					{AgentID: agent.AgentID, Name: "revived", Status: "online"},
				},
			}))

			agents, err := store.ListNodeAgents(ctx, principal, node.NodeID)
			require.NoError(t, err)
			assert.Empty(t, agents)
			deleted, err := store.ListDeletedNodeAgentIDs(ctx, node.NodeID, []string{agent.AgentID})
			require.NoError(t, err)
			assert.Equal(t, []string{agent.AgentID}, deleted)
		},
	)
}

func TestMemoryStoreListDeletedNodeAgentIDs(t *testing.T) {
	ctx := context.Background()
	store, node, agent := sessionReportStoreFixture(t, ctx)
	principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
	second, _, err := store.CreateNodeAgent(ctx, principal, CreateAgentRequest{
		NodeID: node.NodeID, Name: "claude-main", AgentType: "claude-code",
	})
	require.NoError(t, err)

	deleted, err := store.ListDeletedNodeAgentIDs(
		ctx,
		node.NodeID,
		[]string{agent.AgentID, second.AgentID},
	)
	require.NoError(t, err)
	assert.Empty(t, deleted)

	_, err = store.DeleteNodeAgent(ctx, principal, DeleteAgentRequest{
		NodeID: node.NodeID, AgentID: second.AgentID,
	})
	require.NoError(t, err)

	deleted, err = store.ListDeletedNodeAgentIDs(
		ctx,
		node.NodeID,
		[]string{agent.AgentID, second.AgentID},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{second.AgentID}, deleted)
	deleted, err = store.ListDeletedNodeAgentIDs(ctx, "node_other", []string{second.AgentID})
	require.NoError(t, err)
	assert.Empty(t, deleted)
	deleted, err = store.ListDeletedNodeAgentIDs(ctx, node.NodeID, nil)
	require.NoError(t, err)
	assert.Empty(t, deleted)

	// Deleting the node drops its tombstones with it.
	_, err = store.DeleteNode(ctx, principal, DeleteNodeRequest{NodeID: node.NodeID})
	require.NoError(t, err)
	deleted, err = store.ListDeletedNodeAgentIDs(ctx, node.NodeID, []string{second.AgentID})
	require.NoError(t, err)
	assert.Empty(t, deleted)
}

func TestPostgresNodeStatusReconcilesDeletedAgents(t *testing.T) {
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
		CREATE TEMP TABLE nodes (
			node_id text PRIMARY KEY, owner_user_id text, status text, last_heartbeat timestamptz,
			hostname text NOT NULL DEFAULT '', metadata jsonb, machine_type text NOT NULL DEFAULT '',
			os text NOT NULL DEFAULT '', arch text NOT NULL DEFAULT '',
			paxd_version text NOT NULL DEFAULT ''
		);
		CREATE TEMP TABLE agents (
			agent_id text PRIMARY KEY, node_id text, owner_user_id text,
			name text NOT NULL DEFAULT '', description text NOT NULL DEFAULT '',
			card jsonb NOT NULL DEFAULT '{}'::jsonb, hostname text NOT NULL DEFAULT '',
			agent_type text NOT NULL DEFAULT '', machine_type text NOT NULL DEFAULT '',
			os text NOT NULL DEFAULT '', hermes_version text NOT NULL DEFAULT '',
			api_endpoint text NOT NULL DEFAULT '', api_key_hash text UNIQUE NOT NULL,
			status text NOT NULL DEFAULT 'offline', last_heartbeat timestamptz,
			registered_at timestamptz NOT NULL DEFAULT now(), metadata jsonb,
			deleted_at timestamptz, deleted_by_user_id text
		);
		INSERT INTO nodes (node_id, owner_user_id) VALUES ('node_a', 'owner'), ('node_b', 'owner');
		INSERT INTO agents (agent_id, node_id, owner_user_id, name, api_key_hash, status, deleted_at)
		VALUES
			('agent_removed', 'node_a', 'owner', 'removed on node', 'hash_removed', 'online', NULL),
			('agent_kept', 'node_a', 'owner', 'kept', 'hash_kept', 'offline', NULL),
			('agent_foreign', 'node_a', 'intruder', 'foreign owner', 'hash_foreign', 'online', NULL),
			('agent_other_node', 'node_b', 'owner', 'other node', 'hash_other_node', 'online', NULL),
			('agent_tombstoned', 'node_a', 'owner', 'tombstoned', 'hash_tombstoned', 'deleted',
				'2026-01-02T03:04:05Z');
	`)
	require.NoError(t, err)
	now := time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC)
	store := NewPostgresStore(db, func() time.Time { return now })
	node := Node{NodeID: "node_a", OwnerUserID: "owner", Hostname: "node-a"}

	require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
		NodeID: node.NodeID,
		Agents: []AgentStatusInput{
			{AgentID: "agent_kept", Name: "kept renamed", Status: "online"},
			{AgentID: "agent_tombstoned", Name: "revived", Status: "online"},
		},
		DeletedAgentIDs: []string{
			"agent_removed", "agent_foreign", "agent_other_node", "agent_tombstoned", "agent_unknown",
		},
	}))

	type agentRow struct {
		name      string
		status    string
		deletedAt sql.NullTime
		deletedBy sql.NullString
		keyHash   string
		heartbeat sql.NullTime
	}
	readAgent := func(agentID string) agentRow {
		t.Helper()
		var row agentRow
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT name, status, deleted_at, deleted_by_user_id, api_key_hash, last_heartbeat
			FROM agents WHERE agent_id = $1
		`, agentID).Scan(
			&row.name, &row.status, &row.deletedAt, &row.deletedBy, &row.keyHash, &row.heartbeat,
		))
		return row
	}

	removed := readAgent("agent_removed")
	assert.Equal(t, "deleted", removed.status)
	require.True(t, removed.deletedAt.Valid)
	assert.True(t, removed.deletedAt.Time.Equal(now))
	assert.False(t, removed.deletedBy.Valid, "a node-reported removal has no deleting user")
	assert.False(t, removed.heartbeat.Valid)
	assert.Contains(t, removed.keyHash, "revoked:agent:agent_removed:")

	kept := readAgent("agent_kept")
	assert.Equal(t, "kept renamed", kept.name)
	assert.Equal(t, "online", kept.status)
	assert.False(t, kept.deletedAt.Valid)

	// Agents of another owner or node are out of this node's reach.
	for _, agentID := range []string{"agent_foreign", "agent_other_node"} {
		row := readAgent(agentID)
		assert.Equal(t, "online", row.status, agentID)
		assert.False(t, row.deletedAt.Valid, agentID)
	}

	// A tombstone is neither rewritten by the status report nor re-stamped.
	tombstoned := readAgent("agent_tombstoned")
	assert.Equal(t, "tombstoned", tombstoned.name)
	assert.Equal(t, "deleted", tombstoned.status)
	assert.Equal(t, "hash_tombstoned", tombstoned.keyHash)
	assert.True(
		t,
		tombstoned.deletedAt.Time.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	)

	var unknown int
	require.NoError(t, db.QueryRowContext(
		ctx, `SELECT count(*) FROM agents WHERE agent_id = 'agent_unknown'`,
	).Scan(&unknown))
	assert.Zero(t, unknown)

	deleted, err := store.ListDeletedNodeAgentIDs(ctx, "node_a", []string{
		"agent_tombstoned", "agent_removed", "agent_kept", "agent_foreign", "agent_other_node",
		"agent_unknown",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"agent_removed", "agent_tombstoned"}, deleted)
	deleted, err = store.ListDeletedNodeAgentIDs(ctx, "node_b", []string{"agent_removed"})
	require.NoError(t, err)
	assert.Empty(t, deleted)
	deleted, err = store.ListDeletedNodeAgentIDs(ctx, "node_a", nil)
	require.NoError(t, err)
	assert.Empty(t, deleted)
}

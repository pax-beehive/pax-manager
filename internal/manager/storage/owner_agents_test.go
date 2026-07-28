package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreListOwnerAgentsBase(t *testing.T) {
	t.Run(
		"returns only the owner's own agents across all nodes",
		func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })

			owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
			require.NoError(t, err)
			nodeA, err := store.RegisterNode(
				ctx,
				owner,
				RegisterNodeRequest{Name: "node-a", Hostname: "node-a", OS: "linux"},
				"hash_a",
			)
			require.NoError(t, err)
			nodeB, err := store.RegisterNode(
				ctx,
				owner,
				RegisterNodeRequest{Name: "node-b", Hostname: "node-b", OS: "linux"},
				"hash_b",
			)
			require.NoError(t, err)
			a1, _, err := store.CreateNodeAgent(
				ctx,
				UserPrincipal{User: owner},
				CreateAgentRequest{NodeID: nodeA.NodeID, Name: "a1", AgentType: "codex"},
			)
			require.NoError(t, err)
			a2, _, err := store.CreateNodeAgent(
				ctx,
				UserPrincipal{User: owner},
				CreateAgentRequest{NodeID: nodeB.NodeID, Name: "a2", AgentType: "codex"},
			)
			require.NoError(t, err)

			other, err := store.EnsureUser(ctx, "other@example.com", "Other", "user")
			require.NoError(t, err)
			otherNode, err := store.RegisterNode(
				ctx,
				other,
				RegisterNodeRequest{Name: "node-c", Hostname: "node-c", OS: "linux"},
				"hash_c",
			)
			require.NoError(t, err)
			_, _, err = store.CreateNodeAgent(
				ctx,
				UserPrincipal{User: other},
				CreateAgentRequest{NodeID: otherNode.NodeID, Name: "other-agent", AgentType: "codex"},
			)
			require.NoError(t, err)

			got, err := store.ListOwnerAgents(ctx, owner.UserID, OwnerAgentFilter{})
			require.NoError(t, err)

			ids := make([]string, 0, len(got))
			for _, ag := range got {
				ids = append(ids, ag.AgentID)
				assert.Equal(t, owner.UserID, ag.OwnerUserID)
			}
			assert.ElementsMatch(t, []string{a1.AgentID, a2.AgentID}, ids)
		},
	)
}

// ownerAgentFilterFixture creates an owner with a set of named agents on one
// node and returns the store, the owner id, and the agents by name. The clock
// is mutable via the returned pointer so heartbeats can be given distinct
// timestamps.
func ownerAgentFilterFixture(
	t *testing.T,
	ctx context.Context,
	clk *time.Time,
	names ...string,
) (*MemoryStore, string, map[string]Agent) {
	t.Helper()
	store := NewMemoryStore(func() time.Time { return *clk })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	node, err := store.RegisterNode(
		ctx,
		owner,
		RegisterNodeRequest{Name: "node-a", Hostname: "node-a", OS: "linux"},
		"hash_a",
	)
	require.NoError(t, err)
	agents := make(map[string]Agent, len(names))
	for _, name := range names {
		agent, _, err := store.CreateNodeAgent(
			ctx,
			UserPrincipal{User: owner},
			CreateAgentRequest{NodeID: node.NodeID, Name: name, AgentType: "codex"},
		)
		require.NoError(t, err)
		agents[name] = agent
	}
	return store, owner.UserID, agents
}

func ownerAgentIDs(agents []Agent) []string {
	ids := make([]string, 0, len(agents))
	for _, ag := range agents {
		ids = append(ids, ag.AgentID)
	}
	return ids
}

func TestMemoryStoreListOwnerAgentsFilters(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	t.Run("query matches name substring case-insensitively", func(t *testing.T) {
		clk := base
		store, ownerID, agents := ownerAgentFilterFixture(
			t, ctx, &clk, "Backend Bot", "Frontend Helper", "backend-worker",
		)
		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Query: "backend", Status: "any"})
		require.NoError(t, err)
		assert.ElementsMatch(t,
			[]string{agents["Backend Bot"].AgentID, agents["backend-worker"].AgentID},
			ownerAgentIDs(got),
		)
	})

	t.Run("query matches description", func(t *testing.T) {
		clk := base
		store := NewMemoryStore(func() time.Time { return clk })
		owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
		require.NoError(t, err)
		node, err := store.RegisterNode(ctx, owner,
			RegisterNodeRequest{Name: "n", Hostname: "n", OS: "linux"}, "h")
		require.NoError(t, err)
		match, _, err := store.CreateNodeAgent(ctx, UserPrincipal{User: owner},
			CreateAgentRequest{NodeID: node.NodeID, Name: "alpha", Description: "maintains the payments service", AgentType: "codex"})
		require.NoError(t, err)
		_, _, err = store.CreateNodeAgent(ctx, UserPrincipal{User: owner},
			CreateAgentRequest{NodeID: node.NodeID, Name: "beta", Description: "runs the frontend", AgentType: "codex"})
		require.NoError(t, err)

		got, err := store.ListOwnerAgents(ctx, owner.UserID, OwnerAgentFilter{Query: "payments", Status: "any"})
		require.NoError(t, err)
		assert.Equal(t, []string{match.AgentID}, ownerAgentIDs(got))
	})

	t.Run("status online returns only online agents", func(t *testing.T) {
		clk := base
		store, ownerID, agents := ownerAgentFilterFixture(t, ctx, &clk, "on", "off")
		require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{AgentID: agents["on"].AgentID}))

		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Status: "online"})
		require.NoError(t, err)
		assert.Equal(t, []string{agents["on"].AgentID}, ownerAgentIDs(got))
	})

	t.Run("status offline returns only non-online agents", func(t *testing.T) {
		clk := base
		store, ownerID, agents := ownerAgentFilterFixture(t, ctx, &clk, "on", "off")
		require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{AgentID: agents["on"].AgentID}))

		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Status: "offline"})
		require.NoError(t, err)
		assert.Equal(t, []string{agents["off"].AgentID}, ownerAgentIDs(got))
	})

	t.Run("empty status does not filter by status", func(t *testing.T) {
		clk := base
		store, ownerID, agents := ownerAgentFilterFixture(t, ctx, &clk, "on", "off")
		require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{AgentID: agents["on"].AgentID}))

		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{})
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("order_by name sorts alphabetically", func(t *testing.T) {
		clk := base
		store, ownerID, _ := ownerAgentFilterFixture(t, ctx, &clk, "charlie", "alpha", "bravo")
		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Status: "any", OrderBy: "name"})
		require.NoError(t, err)
		names := make([]string, 0, len(got))
		for _, ag := range got {
			names = append(names, ag.Name)
		}
		assert.Equal(t, []string{"alpha", "bravo", "charlie"}, names)
	})

	t.Run("order_by last_active sorts by most recent heartbeat first", func(t *testing.T) {
		clk := base
		store, ownerID, agents := ownerAgentFilterFixture(t, ctx, &clk, "a", "b", "c")
		clk = base.Add(1 * time.Minute)
		require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{AgentID: agents["a"].AgentID}))
		clk = base.Add(3 * time.Minute)
		require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{AgentID: agents["c"].AgentID}))
		clk = base.Add(2 * time.Minute)
		require.NoError(t, store.UpsertAgentStatus(ctx, AgentStatusReport{AgentID: agents["b"].AgentID}))

		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Status: "any", OrderBy: "last_active"})
		require.NoError(t, err)
		assert.Equal(t,
			[]string{agents["c"].AgentID, agents["b"].AgentID, agents["a"].AgentID},
			ownerAgentIDs(got),
		)
	})

	t.Run("relevance ranks exact and prefix above substring", func(t *testing.T) {
		clk := base
		store, ownerID, agents := ownerAgentFilterFixture(
			t, ctx, &clk, "team backend", "backend", "backend service",
		)
		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Query: "backend", Status: "any"})
		require.NoError(t, err)
		// exact "backend" first, then prefix "backend service", then substring "team backend".
		assert.Equal(t,
			[]string{
				agents["backend"].AgentID,
				agents["backend service"].AgentID,
				agents["team backend"].AgentID,
			},
			ownerAgentIDs(got),
		)
	})

	t.Run("limit caps results", func(t *testing.T) {
		clk := base
		store, ownerID, _ := ownerAgentFilterFixture(t, ctx, &clk, "a", "b", "c", "d")
		got, err := store.ListOwnerAgents(ctx, ownerID, OwnerAgentFilter{Status: "any", Limit: 2})
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})
}

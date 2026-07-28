package manager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestListNodeOwnerAgents(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	newStore := func() (*storage.MemoryStore, domain.Node, domain.Agent, string) {
		store := storage.NewMemoryStore(func() time.Time { return now })
		owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
		require.NoError(t, err)
		nodeA, err := store.RegisterNode(ctx, owner,
			domain.RegisterNodeRequest{Name: "node-a", Hostname: "node-a", OS: "linux"}, "hash_a")
		require.NoError(t, err)
		nodeB, err := store.RegisterNode(ctx, owner,
			domain.RegisterNodeRequest{Name: "node-b", Hostname: "node-b", OS: "linux"}, "hash_b")
		require.NoError(t, err)
		caller, _, err := store.CreateNodeAgent(ctx, domain.UserPrincipal{User: owner},
			domain.CreateAgentRequest{NodeID: nodeA.NodeID, Name: "caller", AgentType: "codex"})
		require.NoError(t, err)
		_, _, err = store.CreateNodeAgent(ctx, domain.UserPrincipal{User: owner},
			domain.CreateAgentRequest{NodeID: nodeB.NodeID, Name: "other-node-agent", AgentType: "codex"})
		require.NoError(t, err)
		return store, nodeA, caller, owner.UserID
	}

	t.Run("returns the caller owner's agents across all nodes", func(t *testing.T) {
		store, nodeA, caller, ownerID := newStore()

		got, err := listNodeOwnerAgents(ctx, store, nodeA, caller.AgentID, domain.OwnerAgentFilter{})
		require.NoError(t, err)

		assert.Len(t, got, 2)
		for _, ag := range got {
			assert.Equal(t, ownerID, ag.OwnerUserID)
		}
	})

	t.Run("empty from_agent_id is rejected", func(t *testing.T) {
		store, nodeA, _, _ := newStore()

		_, err := listNodeOwnerAgents(ctx, store, nodeA, "  ", domain.OwnerAgentFilter{})
		require.Error(t, err)
	})

	t.Run("a foreign owner's agent id is not resolvable on the caller node", func(t *testing.T) {
		store, nodeA, _, _ := newStore()
		// Attacker owns their own node and passes a victim's agent id that is
		// not on the caller node.
		victim, err := store.EnsureUser(ctx, "victim@example.com", "Victim", "user")
		require.NoError(t, err)
		victimNode, err := store.RegisterNode(ctx, victim,
			domain.RegisterNodeRequest{Name: "victim-node", Hostname: "victim-node", OS: "linux"}, "hash_v")
		require.NoError(t, err)
		victimAgent, _, err := store.CreateNodeAgent(ctx, domain.UserPrincipal{User: victim},
			domain.CreateAgentRequest{NodeID: victimNode.NodeID, Name: "victim-agent", AgentType: "codex"})
		require.NoError(t, err)

		_, err = listNodeOwnerAgents(ctx, store, nodeA, victimAgent.AgentID, domain.OwnerAgentFilter{})
		require.ErrorIs(t, err, domain.ErrNotFound)
	})
}

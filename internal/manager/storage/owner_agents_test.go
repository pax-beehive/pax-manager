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

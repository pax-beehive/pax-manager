package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportedAgentStatusUsesOnlineFlagWhenStatusIsEmpty(t *testing.T) {
	if got := reportedAgentStatus(AgentStatusInput{}); got != "offline" {
		t.Fatalf("empty status = %q, want offline", got)
	}
	if got := reportedAgentStatus(AgentStatusInput{Online: true}); got != "online" {
		t.Fatalf("online flag status = %q, want online", got)
	}
	if got := reportedAgentStatus(AgentStatusInput{Status: "degraded", Online: true}); got != "degraded" {
		t.Fatalf("explicit status = %q, want degraded", got)
	}
}

func TestMemoryStoreUpsertAgentSessions(t *testing.T) {
	t.Run(
		"Given a node-owned agent when upserting sessions then it writes and updates sessions only",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			before, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.NoError(t, err)

			err = store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID:      "codex:abc",
				NativeID:       "codex:abc",
				AgentType:      "codex",
				SessionName:    "Old",
				ProjectID:      "/workspace/paxd",
				WorkspaceRoots: []string{"/workspace/paxd"},
				Status:         "available",
			}})
			require.NoError(t, err)
			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			sessionID := sessions[0].SessionID
			assert.True(t, isManagerSessionID(sessionID), sessionID)
			assert.Equal(t, "abc", sessions[0].NativeID)
			assert.Equal(t, "Old", sessions[0].SessionName)

			err = store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID:   "codex:abc",
				AgentType:   "codex",
				SessionName: "New",
				Status:      "busy",
			}})
			require.NoError(t, err)
			sessions, err = store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			assert.Equal(t, sessionID, sessions[0].SessionID)
			assert.Equal(t, "New", sessions[0].SessionName)
			assert.Equal(t, "busy", sessions[0].Status)

			after, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, before.Name, after.Name)
			assert.Equal(t, before.AgentType, after.AgentType)
			assert.Equal(t, before.Status, after.Status)
			assert.Equal(t, before.LastHeartbeat, after.LastHeartbeat)
		},
	)

	t.Run(
		"Given another node when upserting sessions then it returns not found",
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

			err = store.UpsertAgentSessions(ctx, otherNode, agent.AgentID, []SessionStatusInput{{
				SessionID: "codex:abc",
			}})

			require.ErrorIs(t, err, ErrNotFound)
		},
	)

	t.Run(
		"Given an existing ACP tunnel session when paxl reports it then it preserves source",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)

			err := store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID: "codex:abc",
				NativeID:  "abc",
				AgentType: "codex",
				Source:    "acp_tunnel",
				Status:    "idle",
			}})
			require.NoError(t, err)

			err = store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID:   "codex:abc",
				NativeID:    "abc",
				AgentType:   "codex",
				SessionName: "Reported by paxl",
				Source:      "paxl",
				Status:      "available",
			}})
			require.NoError(t, err)

			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			assert.Equal(t, "acp_tunnel", sessions[0].Source)
			assert.Equal(t, "Reported by paxl", sessions[0].SessionName)

			err = store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID: "codex:abc",
				NativeID:  "abc",
				AgentType: "codex",
				Source:    "paxl",
				Status:    "available",
			}})
			require.NoError(t, err)

			sessions, err = store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			assert.Equal(t, "Reported by paxl", sessions[0].SessionName)
		},
	)

	t.Run(
		"Given reported sessions are listed then activity time controls ordering",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			newerActivity := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
			olderActivity := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

			err := store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{
				{
					SessionID:     "codex:older",
					AgentType:     "codex",
					SessionName:   "Uploaded later",
					LastMessageAt: &olderActivity,
				},
				{
					SessionID:     "codex:newer",
					AgentType:     "codex",
					SessionName:   "Recently active",
					LastMessageAt: &newerActivity,
				},
			})
			require.NoError(t, err)

			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)

			require.NoError(t, err)
			require.Len(t, sessions, 2)
			assert.Equal(t, "Recently active", sessions[0].SessionName)
			assert.Equal(t, "Uploaded later", sessions[1].SessionName)
		},
	)

	t.Run(
		"Given empty sessions when upserting then it succeeds without changes",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)

			err := store.UpsertAgentSessions(ctx, node, agent.AgentID, nil)

			require.NoError(t, err)
			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			assert.Empty(t, sessions)
		},
	)

	t.Run("Given an empty session ID when upserting then it returns conflict", func(t *testing.T) {
		ctx := context.Background()
		store, node, agent := sessionReportStoreFixture(t, ctx)

		err := store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{}})

		require.ErrorIs(t, err, ErrConflict)
	})
}

func sessionReportStoreFixture(
	t *testing.T,
	ctx context.Context,
) (*MemoryStore, Node, Agent) {
	t.Helper()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(ctx, "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	node, err := store.RegisterNode(
		ctx,
		owner,
		RegisterNodeRequest{Name: "node-a", Hostname: "node-a", OS: "linux"},
		"hash_node_a",
	)
	require.NoError(t, err)
	agent, _, err := store.CreateNodeAgent(
		ctx,
		UserPrincipal{User: owner},
		CreateAgentRequest{
			NodeID:    node.NodeID,
			Name:      "codex-main",
			AgentType: "codex",
		},
	)
	require.NoError(t, err)
	return store, node, agent
}

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
		"Given a session with a primary project when the agent reports it then the primary project is preserved",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}

			session, err := store.CreateNodeAgentSession(ctx, principal, CreateSessionRequest{
				NodeID:           node.NodeID,
				AgentID:          agent.AgentID,
				SessionID:        "sess_primary_project",
				NativeID:         "native_primary_project",
				PrimaryProjectID: "proj_primary",
			})
			require.NoError(t, err)
			assert.Equal(t, "proj_primary", session.PrimaryProjectID)

			err = store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID:   "native_primary_project",
				NativeID:    "native_primary_project",
				AgentType:   "codex",
				SessionName: "Reported later",
				Status:      "busy",
			}})
			require.NoError(t, err)

			sessions, err := store.ListAgentSessions(ctx, principal, agent.AgentID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			assert.Equal(t, "proj_primary", sessions[0].PrimaryProjectID)
			assert.Equal(t, "Reported later", sessions[0].SessionName)
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
		"Given a custom session name when paxl reports again then it preserves the custom name",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}

			err := store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID:   "codex:custom-name",
				AgentType:   "codex",
				SessionName: "Reported first",
			}})
			require.NoError(t, err)
			sessions, err := store.ListAgentSessions(ctx, principal, agent.AgentID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			sessionID := sessions[0].SessionID
			customName := "My custom name"

			updated, err := store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
				NodeID:      node.NodeID,
				AgentID:     agent.AgentID,
				SessionID:   sessionID,
				SessionName: &customName,
			})
			require.NoError(t, err)
			assert.Equal(t, customName, updated.SessionName)
			assert.Equal(t, "Reported first", updated.ReportedSessionName)
			assert.True(t, updated.NameIsCustom)

			err = store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
				SessionID:   "codex:custom-name",
				AgentType:   "codex",
				SessionName: "Reported later",
			}})
			require.NoError(t, err)
			sessions, err = store.ListAgentSessions(ctx, principal, agent.AgentID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			assert.Equal(t, customName, sessions[0].SessionName)
			assert.Equal(t, "Reported later", sessions[0].ReportedSessionName)
			assert.True(t, sessions[0].NameIsCustom)

			updated, err = store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
				NodeID:          node.NodeID,
				AgentID:         agent.AgentID,
				SessionID:       sessionID,
				UseReportedName: true,
			})
			require.NoError(t, err)
			assert.Equal(t, "Reported later", updated.SessionName)
			assert.Equal(t, "Reported later", updated.ReportedSessionName)
			assert.False(t, updated.NameIsCustom)
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
		"Given user prompt times are reported then ordering is stable by the latest prompt",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			latestPrompt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
			olderPrompt := latestPrompt.Add(-time.Hour)
			laterAssistantMessage := latestPrompt.Add(time.Hour)

			require.NoError(t, store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{
				{SessionID: "codex:latest-prompt", NativeID: "latest-prompt", LastUserMessageAt: &latestPrompt},
				{SessionID: "codex:later-output", NativeID: "later-output", LastUserMessageAt: &olderPrompt, LastMessageAt: &laterAssistantMessage},
			}))

			sessions, err := store.ListAgentSessions(ctx, UserPrincipal{User: User{UserID: node.OwnerUserID}}, agent.AgentID)
			require.NoError(t, err)
			var latestIndex, outputIndex = -1, -1
			for i := range sessions {
				switch sessions[i].NativeID {
				case "latest-prompt":
					latestIndex = i
					require.NotNil(t, sessions[i].LastUserMessageAt)
					assert.Equal(t, latestPrompt, *sessions[i].LastUserMessageAt)
				case "later-output":
					outputIndex = i
				}
			}
			require.NotEqual(t, -1, latestIndex)
			require.NotEqual(t, -1, outputIndex)
			assert.Less(t, latestIndex, outputIndex)
		},
	)

	t.Run(
		"Given older or empty activity is reported then the newest activity is preserved",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			newerActivity := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			olderActivity := newerActivity.Add(-time.Hour)

			for _, activity := range []*time.Time{&newerActivity, &olderActivity, nil} {
				err := store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
					SessionID:     "codex:active",
					AgentType:     "codex",
					LastMessageAt: activity,
				}})
				require.NoError(t, err)
			}

			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.NotNil(t, sessions[0].LastMessageAt)
			assert.Equal(t, newerActivity, *sessions[0].LastMessageAt)
		},
	)

	t.Run(
		"Given older user prompt time is reported then the newest prompt is preserved",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			newer := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
			older := newer.Add(-time.Hour)
			for _, promptAt := range []*time.Time{&newer, &older, nil} {
				require.NoError(t, store.UpsertAgentSessions(ctx, node, agent.AgentID, []SessionStatusInput{{
					SessionID: "codex:prompt", LastUserMessageAt: promptAt,
				}}))
			}
			sessions, err := store.ListAgentSessions(ctx, UserPrincipal{User: User{UserID: node.OwnerUserID}}, agent.AgentID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.NotNil(t, sessions[0].LastUserMessageAt)
			assert.Equal(t, newer, *sessions[0].LastUserMessageAt)
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

func TestMemoryStoreUpsertNodeStatusAgentAdoption(t *testing.T) {
	t.Run(
		"Given another owner's agent ID in a status report then the agent is not re-homed",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)
			other, err := store.EnsureUser(ctx, "other@example.com", "Other", "user")
			require.NoError(t, err)
			otherNode, err := store.RegisterNode(
				ctx,
				other,
				RegisterNodeRequest{Name: "node-b", Hostname: "node-b", OS: "linux"},
				"hash_node_b",
			)
			require.NoError(t, err)

			err = store.UpsertNodeStatus(ctx, otherNode, NodeStatusReport{
				NodeID: otherNode.NodeID,
				Agents: []AgentStatusInput{{
					AgentID:  agent.AgentID,
					Name:     "hijacked",
					Status:   "online",
					Sessions: []SessionStatusInput{{SessionID: "codex:evil"}},
				}},
			})
			require.NoError(t, err)

			after, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, node.NodeID, after.NodeID)
			assert.Equal(t, node.OwnerUserID, after.OwnerUserID)
			assert.Equal(t, agent.Name, after.Name)

			_, err = store.GetNodeAgent(ctx, otherNode.NodeID, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)

			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			assert.Empty(t, sessions)
		},
	)

	t.Run(
		"Given another node of the same owner in a status report then the agent stays on its node",
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

			err = store.UpsertNodeStatus(ctx, otherNode, NodeStatusReport{
				NodeID: otherNode.NodeID,
				Agents: []AgentStatusInput{{
					AgentID: agent.AgentID,
					Name:    "hijacked",
					Status:  "online",
				}},
			})
			require.NoError(t, err)

			after, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, node.NodeID, after.NodeID)
			assert.Equal(t, agent.Name, after.Name)

			_, err = store.GetNodeAgent(ctx, otherNode.NodeID, agent.AgentID)
			require.ErrorIs(t, err, ErrNotFound)
		},
	)

	t.Run(
		"Given the owning node when reporting its agent then the report still applies",
		func(t *testing.T) {
			ctx := context.Background()
			store, node, agent := sessionReportStoreFixture(t, ctx)

			err := store.UpsertNodeStatus(ctx, node, NodeStatusReport{
				NodeID: node.NodeID,
				Agents: []AgentStatusInput{{
					AgentID:  agent.AgentID,
					Name:     "codex-renamed",
					Status:   "online",
					Sessions: []SessionStatusInput{{SessionID: "codex:abc"}},
				}},
			})
			require.NoError(t, err)

			after, err := store.GetNodeAgent(ctx, node.NodeID, agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, "codex-renamed", after.Name)
			assert.Equal(t, "online", after.Status)

			sessions, err := store.ListAgentSessions(
				ctx,
				UserPrincipal{User: User{UserID: node.OwnerUserID}},
				agent.AgentID,
			)
			require.NoError(t, err)
			assert.Len(t, sessions, 1)
		},
	)
}

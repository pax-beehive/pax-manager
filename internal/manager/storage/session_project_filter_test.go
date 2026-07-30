package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryListSessionsGivenPrimaryProjectFilterThenReturnsOnlyItsSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	})
	principal := UserPrincipal{User: User{UserID: "user_owner"}}
	store.agents["agent_1"] = Agent{
		AgentID:     "agent_1",
		NodeID:      "node_1",
		OwnerUserID: principal.User.UserID,
	}
	for _, req := range []CreateSessionRequest{
		{
			NodeID:           "node_1",
			AgentID:          "agent_1",
			SessionID:        "sess_project_1",
			PrimaryProjectID: "proj_1",
		},
		{
			NodeID:           "node_1",
			AgentID:          "agent_1",
			SessionID:        "sess_project_2",
			PrimaryProjectID: "proj_2",
		},
		{
			NodeID:    "node_1",
			AgentID:   "agent_1",
			SessionID: "sess_projectless",
		},
	} {
		_, err := store.CreateNodeAgentSession(ctx, principal, req)
		require.NoError(t, err)
	}

	result, err := store.ListSessions(ctx, principal, ListSessionsFilter{
		PrimaryProjectID: "proj_1",
		PageSize:         50,
		PageNum:          1,
	})
	require.NoError(t, err)
	require.Len(t, result.Sessions, 1)
	assert.Equal(t, "sess_project_1", result.Sessions[0].SessionID)
	assert.Equal(t, int64(1), result.Pagination.Total)
}

package storage

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresSessionArchiveGivenArchiveUpdateThenPersistsArchivedAt(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	activeValues := postgresSessionValues("agent_1", "sess_1", "", "", "", now)
	archivedValues := append([]driver.Value(nil), activeValues...)
	archivedValues[len(archivedValues)-2] = &now
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			scriptedRow(activeValues...),
			scriptedRow(archivedValues...),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	archived := true

	updated, err := store.UpdateNodeAgentSession(
		context.Background(),
		UserPrincipal{User: User{UserID: "user_1"}},
		UpdateSessionRequest{
			NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1", Archived: &archived,
		},
	)

	require.NoError(t, err)
	require.NotNil(t, updated.ArchivedAt)
	assert.Equal(t, now, *updated.ArchivedAt)
	require.Len(t, script.execTexts, 1)
	assert.Contains(t, script.execTexts[0], "SET archived_at = CASE WHEN")
}

func TestPostgresSessionListGivenArchiveFilterThenDefaultExcludesAndInclusiveQueryDoesNot(
	t *testing.T,
) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	script := &scriptedPostgresScript{
		queries: []scriptedRows{
			scriptedRow(int64(1)),
			scriptedRow(postgresSessionValues("agent_1", "sess_1", "", "", "", now)...),
			scriptedRow(int64(1)),
			scriptedRow(postgresSessionValues("agent_1", "sess_1", "", "", "", now)...),
		},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	principal := UserPrincipal{User: User{UserID: "user_1"}}

	_, err := store.ListSessions(context.Background(), principal, ListSessionsFilter{
		OwnerUserID: "user_1", PageSize: 20, PageNum: 1,
	})
	require.NoError(t, err)
	_, err = store.ListSessions(context.Background(), principal, ListSessionsFilter{
		OwnerUserID: "user_1", IncludeArchived: true, PageSize: 20, PageNum: 1,
	})
	require.NoError(t, err)

	require.Len(t, script.queryTexts, 4)
	assert.Contains(
		t,
		strings.Join(script.queryTexts[:2], "\n"),
		"agent_sessions.archived_at IS NULL",
	)
	assert.NotContains(
		t,
		strings.Join(script.queryTexts[2:], "\n"),
		"agent_sessions.archived_at IS NULL",
	)
}

func TestMemorySessionArchiveGivenArchivedSessionThenDefaultListHidesAndInclusiveListReturnsIt(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	principal := UserPrincipal{User: User{UserID: "user_owner"}}
	store.agents["agent_1"] = Agent{
		AgentID:     "agent_1",
		NodeID:      "node_1",
		OwnerUserID: principal.User.UserID,
	}
	for _, sessionID := range []string{"sess_active", "sess_archived"} {
		_, err := store.CreateNodeAgentSession(ctx, principal, CreateSessionRequest{
			NodeID:    "node_1",
			AgentID:   "agent_1",
			SessionID: sessionID,
		})
		require.NoError(t, err)
	}

	archived := true
	updated, err := store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
		NodeID:    "node_1",
		AgentID:   "agent_1",
		SessionID: "sess_archived",
		Archived:  &archived,
	})
	require.NoError(t, err)
	require.NotNil(t, updated.ArchivedAt)
	assert.Equal(t, now, *updated.ArchivedAt)

	active, err := store.ListSessions(ctx, principal, ListSessionsFilter{
		PageSize: 50,
		PageNum:  1,
	})
	require.NoError(t, err)
	require.Len(t, active.Sessions, 1)
	assert.Equal(t, "sess_active", active.Sessions[0].SessionID)
	assert.Equal(t, int64(1), active.Pagination.Total)

	all, err := store.ListSessions(ctx, principal, ListSessionsFilter{
		IncludeArchived: true,
		PageSize:        50,
		PageNum:         1,
	})
	require.NoError(t, err)
	require.Len(t, all.Sessions, 2)
	assert.Equal(t, int64(2), all.Pagination.Total)
}

func TestMemorySessionArchiveGivenArchivedSessionWhenRestoredThenItReturnsToDefaultList(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	principal := UserPrincipal{User: User{UserID: "user_owner"}}
	store.agents["agent_1"] = Agent{
		AgentID:     "agent_1",
		NodeID:      "node_1",
		OwnerUserID: principal.User.UserID,
	}
	_, err := store.CreateNodeAgentSession(ctx, principal, CreateSessionRequest{
		NodeID:    "node_1",
		AgentID:   "agent_1",
		SessionID: "sess_1",
	})
	require.NoError(t, err)

	archived := true
	_, err = store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
		NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1", Archived: &archived,
	})
	require.NoError(t, err)

	archived = false
	restored, err := store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
		NodeID: "node_1", AgentID: "agent_1", SessionID: "sess_1", Archived: &archived,
	})
	require.NoError(t, err)
	assert.Nil(t, restored.ArchivedAt)

	active, err := store.ListSessions(ctx, principal, ListSessionsFilter{
		PageSize: 50,
		PageNum:  1,
	})
	require.NoError(t, err)
	require.Len(t, active.Sessions, 1)
	assert.Equal(t, "sess_1", active.Sessions[0].SessionID)
}

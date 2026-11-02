package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemorySessionListsGivenInternalWorkThenHideBeforePaginationButAllowDirectRead(
	t *testing.T,
) {
	t.Parallel()
	for _, marker := range []string{"source", "reserved-prefix", "registered"} {
		t.Run(marker, func(t *testing.T) {
			now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
			store := NewMemoryStore(func() time.Time { return now })
			principal := UserPrincipal{User: User{UserID: "owner"}}
			store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
			internal := AgentSession{AgentID: "agent", SessionID: "internal", UpdatedAt: now}
			switch marker {
			case "source":
				internal.Source = domain.SecretaryInternalSource
			case "reserved-prefix":
				internal.SessionID = domain.SecretaryInternalSessionPrefix + "run"
			case "registered":
				store.secretaryLocked().internal[sessionKey("agent", internal.SessionID)] = true
			}
			store.sessions[sessionKey("agent", internal.SessionID)] = internal
			for index, id := range []string{"normal-1", "normal-2"} {
				store.sessions[sessionKey("agent", id)] = AgentSession{
					AgentID: "agent", SessionID: id, UpdatedAt: now.Add(-time.Duration(index+1) * time.Hour),
				}
			}
			for _, archived := range []bool{false, true} {
				for page := 1; page <= 2; page++ {
					result, err := store.ListSessions(t.Context(), principal, ListSessionsFilter{
						PageSize: 1, PageNum: page, IncludeArchived: archived,
					})
					require.NoError(t, err)
					require.Len(t, result.Sessions, 1)
					assert.Equal(
						t,
						[]string{"normal-1", "normal-2"}[page-1],
						result.Sessions[0].SessionID,
					)
					assert.EqualValues(t, 2, result.Pagination.Total)
					assert.Equal(t, 2, result.Pagination.TotalPages)
				}
			}
			listed, err := store.ListAgentSessions(t.Context(), principal, "agent")
			require.NoError(t, err)
			require.Len(t, listed, 2)
			assert.Equal(t, "normal-1", listed[0].SessionID)
			read, err := store.GetSession(t.Context(), principal, internal.SessionID)
			require.NoError(t, err)
			assert.Equal(t, internal.SessionID, read.SessionID)
			_, err = store.GetSession(
				t.Context(),
				UserPrincipal{User: User{UserID: "other"}},
				internal.SessionID,
			)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestMemorySessionListsGivenSimilarNamesAndOtherAgentRegistryThenKeepOrdinarySessions(
	t *testing.T,
) {
	t.Parallel()
	store := NewMemoryStore(time.Now)
	principal := UserPrincipal{User: User{UserID: "owner"}}
	store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
	for _, id := range []string{"sess-secretary-run", "sess_secretary", "normal"} {
		store.sessions[sessionKey("agent", id)] = AgentSession{
			AgentID:     "agent",
			SessionID:   id,
			SessionName: "Secretary",
		}
	}
	store.secretaryLocked().internal[sessionKey("other-agent", "normal")] = true
	result, err := store.ListSessions(t.Context(), principal, ListSessionsFilter{})
	require.NoError(t, err)
	assert.Len(t, result.Sessions, 3)
	listed, err := store.ListAgentSessions(t.Context(), principal, "agent")
	require.NoError(t, err)
	assert.Len(t, listed, 3)
}

func TestPostgresSessionListsGivenInternalMarkersThenFilterCountAndRowsButKeepDirectRead(
	t *testing.T,
) {
	values := postgresSessionValues("agent", "sess_secretary_run", "", "", "", time.Now())
	script := &scriptedPostgresScript{queries: []scriptedRows{
		scriptedRow(int64(0)),
		summaryEmptyRows(),
		summaryEmptyRows(),
		scriptedRow(values...),
	}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	principal := UserPrincipal{User: User{UserID: "owner"}}
	_, err := store.ListSessions(t.Context(), principal, ListSessionsFilter{IncludeArchived: true})
	require.NoError(t, err)
	_, err = store.ListAgentSessions(t.Context(), principal, "agent")
	require.NoError(t, err)
	read, err := store.GetSession(t.Context(), principal, "sess_secretary_run")
	require.NoError(t, err)
	assert.Equal(t, "sess_secretary_run", read.SessionID)
	require.Len(t, script.queryTexts, 4)
	for _, query := range script.queryTexts[:3] {
		assert.Contains(t, query, "COALESCE(agent_sessions.source, '') <> 'secretary_internal'")
		assert.Contains(t, query, "NOT starts_with(agent_sessions.session_id, 'sess_secretary_')")
		assert.Contains(t, query, "FROM secretary_internal_sessions")
		assert.Contains(t, query, "i.agent_id = agent_sessions.agent_id")
		assert.Contains(t, query, "i.session_id = agent_sessions.session_id")
	}
	assert.NotContains(t, script.queryTexts[3], "secretary_internal")
}

func TestPostgresSessionListsGivenInternalWorkThenHideEachMarkerAndKeepRegistryAgentScope(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
 INSERT INTO agent_sessions(agent_id,session_id,source) VALUES
 ('source','source-marked','secretary_internal'),
 ('source','sess_secretary_reserved',NULL),
 ('source','registered',NULL),
 ('source','same-id',NULL),
 ('secretary','same-id',NULL);
 INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES
 ('source','registered','owner'),('secretary','same-id','owner');`)
	require.NoError(t, err)
	principal := UserPrincipal{User: User{UserID: "owner"}}
	for _, includeArchived := range []bool{false, true} {
		result, listErr := store.ListSessions(t.Context(), principal, ListSessionsFilter{
			IncludeArchived: includeArchived, PageSize: 1, PageNum: 1,
		})
		require.NoError(t, listErr)
		assert.Len(t, result.Sessions, 1)
		assert.EqualValues(t, 2, result.Pagination.Total)
		assert.Equal(t, 2, result.Pagination.TotalPages)
	}
	listed, err := store.ListAgentSessions(t.Context(), principal, "source")
	require.NoError(t, err)
	assert.Len(t, listed, 2)
	for _, id := range []string{"source-marked", "sess_secretary_reserved", "registered"} {
		read, readErr := store.GetSession(t.Context(), principal, id)
		require.NoError(t, readErr)
		assert.Equal(t, id, read.SessionID)
		_, readErr = store.GetSession(t.Context(), UserPrincipal{User: User{UserID: "other"}}, id)
		assert.ErrorIs(t, readErr, ErrNotFound)
	}
}

package storage

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemorySessionAliasesGivenInternalHistoryThenResolveOnlyWithinOwnerAndAgent(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore(func() time.Time { return now })
	store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
	store.agents["other-agent"] = Agent{AgentID: "other-agent", OwnerUserID: "owner"}
	internal := AgentSession{
		AgentID:    "agent",
		SessionID:  "sess_secretary_run",
		NativeID:   "native",
		Source:     domain.SecretaryInternalSource,
		ArchivedAt: &now,
	}
	store.sessions[sessionKey("agent", internal.SessionID)] = internal
	store.sessions[sessionKey("agent", "without-native")] = AgentSession{
		AgentID:   "agent",
		SessionID: "without-native",
	}
	for _, id := range []string{internal.SessionID, internal.NativeID} {
		got, err := store.ResolveSessionAliases(t.Context(), "owner", "agent", id)
		require.NoError(t, err)
		assert.Equal(
			t,
			domain.SessionAliases{SessionID: internal.SessionID, NativeID: internal.NativeID},
			got,
		)
	}
	got, err := store.ResolveSessionAliases(t.Context(), "owner", "agent", "without-native")
	require.NoError(t, err)
	assert.Equal(t, "without-native", got.SessionID)
	assert.Empty(t, got.NativeID)
	for _, tc := range []struct{ owner, agent, id string }{
		{"other", "agent", "native"}, {"", "agent", "native"}, {"owner", "agent", ""},
		{"owner", "other-agent", "native"}, {"owner", "missing", "native"}, {"owner", "agent", "missing"},
	} {
		_, err := store.ResolveSessionAliases(t.Context(), tc.owner, tc.agent, tc.id)
		assert.ErrorIs(t, err, ErrNotFound, "%+v", tc)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.ResolveSessionAliases(ctx, "owner", "agent", "native")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMemorySessionAliasesGivenAmbiguousNativeThenPreferCanonicalOrLatestMapping(t *testing.T) {
	store := NewMemoryStore(time.Now)
	store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
	now := time.Now().UTC()
	for _, session := range []AgentSession{
		{AgentID: "agent", SessionID: "older", NativeID: "native", UpdatedAt: now.Add(-time.Hour)},
		{AgentID: "agent", SessionID: "latest-b", NativeID: "native", UpdatedAt: now},
		{AgentID: "agent", SessionID: "latest-a", NativeID: "native", UpdatedAt: now},
	} {
		store.sessions[sessionKey(session.AgentID, session.SessionID)] = session
	}
	got, err := store.ResolveSessionAliases(t.Context(), "owner", "agent", "native")
	require.NoError(t, err)
	assert.Equal(t, "latest-a", got.SessionID)
	store.sessions[sessionKey("agent", "native")] = AgentSession{
		AgentID:   "agent",
		SessionID: "native",
		NativeID:  "other-native",
	}
	got, err = store.ResolveSessionAliases(t.Context(), "owner", "agent", "native")
	require.NoError(t, err)
	assert.Equal(t, "native", got.SessionID)
	assert.Equal(t, "other-native", got.NativeID)
}

func TestMemorySessionAliasesGivenTeamAccessThenPreserveAuthorizedRouting(t *testing.T) {
	store := NewMemoryStore(time.Now)
	store.agents["agent"] = Agent{AgentID: "agent", OwnerUserID: "owner"}
	store.sessions[sessionKey("agent", "manager")] = AgentSession{
		AgentID:   "agent",
		SessionID: "manager",
		NativeID:  "native",
	}
	store.teams["team"] = Team{TeamID: "team", Status: domain.TeamStatusActive}
	store.teamAgents[teamAgentKey{TeamID: "team", AgentID: "agent"}] = TeamAgent{
		TeamID:  "team",
		AgentID: "agent",
	}
	key := teamMemberKey{TeamID: "team", UserID: "member"}
	store.teamMembers[key] = TeamMember{
		TeamID: "team",
		UserID: "member",
		Status: domain.TeamMemberStatusActive,
	}
	got, err := store.ResolveSessionAliases(t.Context(), "member", "agent", "native")
	require.NoError(t, err)
	assert.Equal(t, "manager", got.SessionID)
	store.teamMembers[key] = TeamMember{
		TeamID: "team",
		UserID: "member",
		Status: domain.TeamMemberStatusRemoved,
	}
	_, err = store.ResolveSessionAliases(t.Context(), "member", "agent", "native")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPostgresSessionAliasesGivenInternalWorkThenQueryOnlyScopedRoutingColumns(t *testing.T) {
	script := &scriptedPostgresScript{queries: []scriptedRows{{
		columns: []string{
			"session_id",
			"native_id",
		}, values: [][]driver.Value{{"sess_secretary_run", "native"}},
	}}}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()
	got, err := store.ResolveSessionAliases(t.Context(), "owner", "agent", "native")
	require.NoError(t, err)
	assert.Equal(t, domain.SessionAliases{SessionID: "sess_secretary_run", NativeID: "native"}, got)
	require.Len(t, script.queryTexts, 1)
	query := script.queryTexts[0]
	assert.Contains(t, query, "s.agent_id = $1 AND a.deleted_at IS NULL")
	assert.Contains(t, query, "a.owner_user_id = $2 OR")
	assert.Contains(t, query, teamAgentAccessSQL("a.agent_id", "$2"))
	assert.Contains(t, query, "s.session_id IN ($3, COALESCE(")
	assert.Contains(t, query, "b.agent_id = $1 AND b.native_session_id = $3")
	assert.Contains(t, query, "n.agent_id = $1 AND n.native_id = $3")
	assert.Contains(t, query, "LIMIT 1")
	assert.NotContains(t, query, "secretary_internal")
	assert.NotContains(t, query, "archived_at")
	assert.NotContains(t, query, "metadata")
	assert.Equal(t, "agent", script.queryArgs[0][0].Value)
	assert.Equal(t, "owner", script.queryArgs[0][1].Value)
	assert.Equal(t, "native", script.queryArgs[0][2].Value)
}

func TestPostgresSessionAliasesGivenLookupFailuresThenReturnFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []scriptedRows
		want error
	}{
		{name: "missing", rows: []scriptedRows{{columns: []string{"session_id", "native_id"}}}, want: ErrNotFound},
		{name: "query failure"},
		{name: "scan failure", rows: []scriptedRows{{columns: []string{"session_id"}, values: [][]driver.Value{{"bad"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := scriptedPostgresStore(t, &scriptedPostgresScript{queries: tc.rows})
			defer cleanup()
			_, err := store.ResolveSessionAliases(t.Context(), "owner", "agent", "native")
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			} else {
				assert.Error(t, err)
			}
		})
	}
	store := &PostgresStore{}
	for _, args := range [][3]string{{"", "agent", "native"}, {"owner", "", "native"}, {"owner", "agent", ""}} {
		_, err := store.ResolveSessionAliases(t.Context(), args[0], args[1], args[2])
		assert.ErrorIs(t, err, ErrNotFound)
	}
}

func TestPostgresSessionAliasesGivenHiddenSessionThenOwnerCanResolveBothRoutingIDs(t *testing.T) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(
		t.Context(),
		`INSERT INTO agent_sessions(agent_id,session_id,native_id,source,archived_at)
		VALUES ('source','sess_secretary_alias','native-alias','secretary_internal',now())`,
	)
	require.NoError(t, err)
	for _, id := range []string{"sess_secretary_alias", "native-alias"} {
		got, err := store.ResolveSessionAliases(t.Context(), "owner", "source", id)
		require.NoError(t, err)
		assert.Equal(
			t,
			domain.SessionAliases{SessionID: "sess_secretary_alias", NativeID: "native-alias"},
			got,
		)
	}
	for _, args := range [][3]string{{"other", "source", "native-alias"}, {"owner", "secretary", "native-alias"}} {
		_, err := store.ResolveSessionAliases(t.Context(), args[0], args[1], args[2])
		assert.ErrorIs(t, err, ErrNotFound)
	}
	_, err = store.db.ExecContext(
		t.Context(),
		`UPDATE agents SET deleted_at=now() WHERE agent_id='source'`,
	)
	require.NoError(t, err)
	_, err = store.ResolveSessionAliases(t.Context(), "owner", "source", "native-alias")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPostgresSessionAliasesGivenNewerArchivedDuplicateThenPreferAuthoritativeBinding(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	_, err := store.db.ExecContext(t.Context(), `
		INSERT INTO agent_sessions(agent_id,session_id,native_id,source,updated_at,archived_at) VALUES
		('source','sess_secretary_bound','shared-native','secretary_internal',now()-interval '1 day',NULL),
		('source','sess_secretary_archived','shared-native','secretary_internal',now(),now());
		INSERT INTO agent_native_session_bindings(agent_id,native_session_id,session_id)
		VALUES ('source','shared-native','sess_secretary_bound')
	`)
	require.NoError(t, err)
	got, err := store.ResolveSessionAliases(t.Context(), "owner", "source", "shared-native")
	require.NoError(t, err)
	assert.Equal(t, "sess_secretary_bound", got.SessionID)
	got, err = store.ResolveSessionAliases(
		t.Context(),
		"owner",
		"source",
		"sess_secretary_archived",
	)
	require.NoError(t, err)
	assert.Equal(t, "sess_secretary_archived", got.SessionID, "exact manager IDs take precedence")
}

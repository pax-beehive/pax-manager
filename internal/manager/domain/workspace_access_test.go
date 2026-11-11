package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceAccessNormalizationPreservesAbsentPreauthorization(t *testing.T) {
	scope, err := NormalizeWorkspaceSearchAccess(nil)
	require.NoError(t, err)
	assert.Nil(t, scope)
	scope, err = NormalizeWorkspaceSearchAccess(&WorkspaceSearchAccess{})
	require.NoError(t, err)
	assert.Equal(
		t,
		&WorkspaceSearchAccess{
			Queries:    []string{"*"},
			AgentIDs:   []string{"*"},
			SessionIDs: []string{"*"},
		},
		scope,
	)
	for _, invalid := range []*WorkspaceSearchAccess{
		{Queries: []string{}}, {SessionIDs: []string{}}, {Queries: []string{"*", "cicd"}},
		{SessionIDs: []string{"*", "session"}}, {Queries: []string{" "}},
		{Queries: []string{strings.Repeat("a", 257)}}, {SessionIDs: []string{strings.Repeat("s", 4097)}},
	} {
		_, err = NormalizeWorkspaceSearchAccess(invalid)
		require.Error(t, err)
	}
}

func TestWorkspaceAccessRequiresCompleteQueryAndEnforcesSessionScope(t *testing.T) {
	scope, err := NormalizeWorkspaceSearchAccess(&WorkspaceSearchAccess{
		Queries: []string{
			" CIcd ",
			"deploy  pipeline",
			"CIcd",
			"ci*",
		}, SessionIDs: []string{"session-b", "session-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ci*", "cicd", "deploy pipeline"}, scope.Queries)
	for _, query := range []string{" CICD ", "deploy \t pipeline", "ci*"} {
		assert.True(t, scope.AllowsQuery(query), query)
	}
	for _, query := range []string{"cicd password", "cicd OR secrets", "deployment", "", "pipeline deploy"} {
		assert.False(t, scope.AllowsQuery(query), query)
	}
	assert.True(
		t,
		scope.Covers(
			WorkspaceSearchScope{AgentIDs: []string{"agent"}, SessionIDs: []string{"session-b"}},
		),
	)
	assert.False(
		t,
		scope.Covers(
			WorkspaceSearchScope{
				AgentIDs:   []string{"agent"},
				SessionIDs: []string{"session-a", "foreign"},
			},
		),
	)
	assert.False(
		t,
		scope.Covers(WorkspaceSearchScope{AgentIDs: []string{"agent"}, SessionIDs: []string{"*"}}),
	)
}

func TestWorkspaceAccessRequiresExplicitScopeAndResolvesSelf(t *testing.T) {
	for _, scope := range []*WorkspaceSearchScope{
		nil, {}, {AgentIDs: []string{"self"}}, {SessionIDs: []string{"*"}},
		{AgentIDs: []string{"*", "self"}, SessionIDs: []string{"*"}},
	} {
		_, err := NormalizeWorkspaceSearchScope(scope, "agent-current")
		require.Error(t, err)
	}
	scope, err := NormalizeWorkspaceSearchScope(&WorkspaceSearchScope{
		AgentIDs: []string{"self", "agent-current"}, SessionIDs: []string{"*"},
	}, "agent-current")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-current"}, scope.AgentIDs)
	grant := WorkspaceSearchAccess{AgentIDs: []string{"agent-current"}, SessionIDs: []string{"*"}}
	assert.True(t, grant.Covers(scope))
	assert.False(
		t,
		grant.Covers(WorkspaceSearchScope{AgentIDs: []string{"*"}, SessionIDs: []string{"*"}}),
	)
}

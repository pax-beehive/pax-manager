package storage

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestLatestPermissionObservationGivenMultipleAgentsWhenReadThenKeepsLatestAndClones(
	t *testing.T,
) {
	now := time.Now().UTC()
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(
		t.Context(),
		owner,
		domain.RegisterAgentRequest{Name: "Claude", AgentType: "claude-code"},
		"key",
	)
	require.NoError(t, err)
	other, err := store.RegisterAgent(
		t.Context(),
		owner,
		domain.RegisterAgentRequest{Name: "Other", AgentType: "claude-code"},
		"other-key",
	)
	require.NoError(t, err)
	_, err = store.GetLatestPermissionObservation(t.Context(), agent.AgentID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	for _, o := range []domain.AgentPermissionObservation{
		{AgentID: agent.AgentID, IdentityFingerprint: "old", ObservedAt: now.Add(-time.Hour)},
		{AgentID: agent.AgentID, IdentityFingerprint: "current", ObservedAt: now, Catalog: domain.ObservedPermissionCatalog{Options: []domain.ObservedPermissionOption{{Value: "plan"}}}},
		{AgentID: other.AgentID, IdentityFingerprint: "foreign", ObservedAt: now.Add(time.Hour)},
	} {
		_, err = store.UpsertPermissionObservation(t.Context(), o)
		require.NoError(t, err)
	}
	observation, err := store.GetLatestPermissionObservation(t.Context(), agent.AgentID)
	require.NoError(t, err)
	assert.Equal(t, "current", observation.IdentityFingerprint)
	observation.Catalog.Options[0].Value = "mutated"
	again, err := store.GetLatestPermissionObservation(t.Context(), agent.AgentID)
	require.NoError(t, err)
	assert.Equal(t, "plan", again.Catalog.Options[0].Value)
	_, err = store.UpsertPermissionObservation(
		t.Context(),
		domain.AgentPermissionObservation{
			AgentID:             agent.AgentID,
			IdentityFingerprint: "negative",
			ObservedAt:          now.Add(time.Minute),
		},
	)
	require.NoError(t, err)
	latest, err := store.GetLatestPermissionObservation(t.Context(), agent.AgentID)
	require.NoError(t, err)
	assert.Empty(t, latest.Catalog.Options, "latest negative must not resurrect an earlier catalog")
}

func TestLatestPermissionObservationGivenPostgresRowsWhenReadThenReturnsLatestOrError(
	t *testing.T,
) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name      string
		rows      [][]driver.Value
		wantError bool
	}{
		{name: "catalog", rows: [][]driver.Value{{"agent-1", "old", int64(1), "hash", []byte(`{"options":[{"value":"plan"}]}`), now, now.Add(time.Hour)}}},
		{name: "missing", wantError: true},
		{name: "invalid catalog", rows: [][]driver.Value{{"agent-1", "old", int64(1), "hash", []byte(`broken`), now, now.Add(time.Hour)}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := &scriptedPostgresScript{
				queries: []scriptedRows{
					{
						columns: []string{
							"agent_id",
							"identity_fingerprint",
							"catalog_revision",
							"catalog_hash",
							"catalog",
							"observed_at",
							"expires_at",
						},
						values: tc.rows,
					},
				},
			}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			observation, err := store.GetLatestPermissionObservation(t.Context(), "agent-1")
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, observation.Catalog.Options, 1)
			assert.Equal(t, "plan", observation.Catalog.Options[0].Value)
			assert.Contains(t, script.queryTexts[0], "WHERE agent_id = $1")
			assert.Contains(
				t,
				script.queryTexts[0],
				"ORDER BY observed_at DESC, identity_fingerprint DESC",
			)
		})
	}
}

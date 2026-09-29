package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// This opt-in test uses its own schema in an isolated test PostgreSQL instance.
func TestPermissionCacheGivenLegacyPostgresSchemaWhenMigratedThenPreservesObservations(
	t *testing.T,
) {
	dsn := os.Getenv("PAX_MANAGER_PERMISSION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PAX_MANAGER_PERMISSION_TEST_DATABASE_URL to isolated PostgreSQL")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	admin := stdlib.OpenDB(*config)
	defer func() { _ = admin.Close() }()
	schema := fmt.Sprintf("permission_cache_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	defer func() { _ = db.Close() }()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	sql := string(raw)
	identityDDL := sql[strings.Index(sql, "CREATE TABLE IF NOT EXISTS agent_runtime_identities ("):strings.Index(sql, "CREATE TABLE IF NOT EXISTS agent_permission_profiles (")]
	observationDDL := sql[strings.Index(sql, "CREATE TABLE IF NOT EXISTS agent_permission_observations ("):strings.Index(sql, "CREATE TABLE IF NOT EXISTS agent_native_session_bindings (")]
	legacyDDL := strings.ReplaceAll(
		identityDDL,
		"    configuration_fingerprint TEXT NOT NULL DEFAULT '',\n",
		"",
	)
	legacyDDL = strings.ReplaceAll(
		legacyDDL,
		"ALTER TABLE agent_runtime_identities\n    ADD COLUMN IF NOT EXISTS configuration_fingerprint TEXT NOT NULL DEFAULT '';\n",
		"",
	)
	_, err = db.ExecContext(
		t.Context(),
		"CREATE TABLE agents (agent_id TEXT PRIMARY KEY); INSERT INTO agents VALUES ('claude-air'), ('other');"+legacyDDL+observationDDL,
	)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { return now })
	legacy := domain.AgentPermissionObservation{
		AgentID:             "claude-air",
		IdentityFingerprint: "sha256:legacy",
		CatalogHash:         "old",
		ObservedAt:          now,
		ExpiresAt:           now.Add(time.Hour),
		Catalog: domain.ObservedPermissionCatalog{
			Options: []domain.ObservedPermissionOption{{Value: "plan", Name: "Plan Mode"}},
		},
	}
	_, err = store.UpsertPermissionObservation(t.Context(), legacy)
	require.NoError(t, err)
	for range 2 {
		_, err = db.ExecContext(t.Context(), identityDDL)
		require.NoError(t, err, "migration must be repeatable")
	}
	identity := domain.AgentRuntimeIdentity{
		AgentID:                  "claude-air",
		SchemaVersion:            2,
		ConnectionID:             "conn",
		ReportGeneration:         1,
		IdentityFingerprint:      "full-new",
		ConfigurationFingerprint: "config-v1:stable",
		PoolConsistency:          "consistent",
		ObservedAt:               now,
	}
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
	loaded, err := store.GetAgentRuntimeIdentity(t.Context(), "claude-air")
	require.NoError(t, err)
	loaded.ObservedAt = loaded.ObservedAt.UTC()
	assert.Equal(t, identity, loaded)
	prior, err := store.GetLatestPermissionObservation(t.Context(), "claude-air")
	require.NoError(t, err)
	assert.Equal(t, legacy.IdentityFingerprint, prior.IdentityFingerprint)
	assert.Equal(t, legacy.Catalog, prior.Catalog)
	foreign := legacy
	foreign.AgentID = "other"
	foreign.ObservedAt = now.Add(time.Hour)
	_, err = store.UpsertPermissionObservation(t.Context(), foreign)
	require.NoError(t, err)
	prior, err = store.GetLatestPermissionObservation(t.Context(), "claude-air")
	require.NoError(t, err)
	assert.Equal(t, "claude-air", prior.AgentID)
	current := legacy
	current.IdentityFingerprint = identity.ConfigurationFingerprint
	current.ObservedAt = now.Add(time.Minute)
	_, err = store.UpsertPermissionObservation(t.Context(), current)
	require.NoError(t, err)
	exact, err := store.GetPermissionObservation(
		t.Context(),
		"claude-air",
		identity.ConfigurationFingerprint,
	)
	require.NoError(t, err)
	assert.Equal(t, current.Catalog, exact.Catalog)
	latest, err := store.GetLatestPermissionObservation(t.Context(), "claude-air")
	require.NoError(t, err)
	assert.Equal(t, current.IdentityFingerprint, latest.IdentityFingerprint)
}

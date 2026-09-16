package storage

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestPermissionStoreGivenProfilesAndObservationsWhenWrittenThenVersionsAreAppendOnly(
	t *testing.T,
) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(t.Context(), owner, domain.RegisterAgentRequest{
		Name: "codex", AgentType: "codex", Hostname: "host", OS: "linux",
	}, "key")
	require.NoError(t, err)

	t.Run(
		"given runtime identities then stale reports cannot replace a newer generation",
		func(t *testing.T) {
			identity := domain.AgentRuntimeIdentity{
				AgentID:             agent.AgentID,
				ReportEpoch:         "epoch-1",
				SchemaVersion:       2,
				ConnectionID:        "conn-1",
				ReportGeneration:    2,
				IdentityFingerprint: "identity-new",
				ObservedAt:          now,
			}
			require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
			identity.ReportGeneration = 1
			identity.IdentityFingerprint = "identity-stale"
			require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
			stored, err := store.GetAgentRuntimeIdentity(t.Context(), agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, "identity-new", stored.IdentityFingerprint)

			identity.ConnectionID = "conn-2"
			identity.IdentityFingerprint = "identity-new-connection"
			require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
			stored, err = store.GetAgentRuntimeIdentity(t.Context(), agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, "identity-new-connection", stored.IdentityFingerprint)

			identity.ConnectionID = "conn-1"
			identity.ReportEpoch = "epoch-2"
			identity.ReportGeneration = 1
			identity.IdentityFingerprint = "identity-after-restart"
			require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
			stored, err = store.GetAgentRuntimeIdentity(t.Context(), agent.AgentID)
			require.NoError(t, err)
			assert.Equal(t, "identity-after-restart", stored.IdentityFingerprint)
		},
	)

	t.Run("given builtin seed then duplicate revision is not overwritten", func(t *testing.T) {
		profiles, err := store.ListActivePermissionProfiles(t.Context(), owner.UserID, "codex")
		require.NoError(t, err)
		require.NotEmpty(t, profiles)
		assert.Equal(t, "codex-acp.permissions", profiles[0].ProfileID)
		assert.Equal(t, int64(1), profiles[0].Revision)

		duplicate := domain.BuiltInCodexPermissionProfile(now.Add(time.Hour))
		duplicate.Definition.DefaultChoiceID = "agent:mode:read-only"
		err = store.InsertPermissionProfile(t.Context(), duplicate)
		assert.True(t, errors.Is(err, ErrConflict))

		profiles, err = store.ListActivePermissionProfiles(t.Context(), owner.UserID, "codex")
		require.NoError(t, err)
		assert.Equal(t, "agent:mode:agent", profiles[0].Definition.DefaultChoiceID)
	})

	t.Run(
		"given a new profile revision then keeps both and selects newest first",
		func(t *testing.T) {
			next := domain.BuiltInCodexPermissionProfile(now)
			next.Revision = 2
			next.Definition.DefaultChoiceID = "agent:mode:read-only"
			require.NoError(t, store.InsertPermissionProfile(t.Context(), next))
			profiles, err := store.ListActivePermissionProfiles(t.Context(), owner.UserID, "codex")
			require.NoError(t, err)
			require.Len(t, profiles, 2)
			assert.Equal(t, int64(2), profiles[0].Revision)
			assert.Equal(t, int64(1), profiles[1].Revision)
		},
	)

	t.Run("given observations then revisions change only when content changes", func(t *testing.T) {
		observation := domain.AgentPermissionObservation{
			AgentID:             agent.AgentID,
			IdentityFingerprint: "identity-1",
			CatalogHash:         "hash-1",
			Catalog: domain.ObservedPermissionCatalog{
				Binding: domain.PermissionBinding{
					Kind:     domain.PermissionBindingConfigOption,
					ConfigID: "mode",
				},
				Options: []domain.ObservedPermissionOption{{Value: "agent", Name: "Agent"}},
			},
			ObservedAt: now,
			ExpiresAt:  now.Add(time.Hour),
		}
		created, err := store.UpsertPermissionObservation(t.Context(), observation)
		require.NoError(t, err)
		assert.Equal(t, int64(1), created.CatalogRevision)

		observation.ObservedAt = now.Add(time.Minute)
		unchanged, err := store.UpsertPermissionObservation(t.Context(), observation)
		require.NoError(t, err)
		assert.Equal(t, int64(1), unchanged.CatalogRevision)

		observation.CatalogHash = "hash-2"
		observation.Catalog.Options[0].Name = "Agent live"
		changed, err := store.UpsertPermissionObservation(t.Context(), observation)
		require.NoError(t, err)
		assert.Equal(t, int64(2), changed.CatalogRevision)

		stored, err := store.GetPermissionObservation(
			t.Context(),
			agent.AgentID,
			"identity-1",
		)
		require.NoError(t, err)
		assert.Equal(t, "Agent live", stored.Catalog.Options[0].Name)
	})
}

func TestPermissionRuntimeIdentityGivenPaxdRestartWhenSnapshotAppliedThenUsesRuntimeFenceEpoch(
	t *testing.T,
) {
	ctx := t.Context()
	store, node, agent := sessionReportStoreFixture(t, ctx)
	require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence-old"))
	old := domain.AgentRuntimeIdentity{
		AgentID:             agent.AgentID,
		ReportEpoch:         "fence-old",
		SchemaVersion:       2,
		ConnectionID:        "durable-connection",
		ReportGeneration:    9,
		IdentityFingerprint: "identity-old",
		PoolConsistency:     "consistent",
		ObservedAt:          time.Now().UTC(),
	}
	require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
		NodeID:       node.NodeID,
		RuntimeFence: "fence-old",
		Agents: []AgentStatusInput{{
			AgentID: agent.AgentID, RuntimeIdentity: &old,
		}},
	}))

	require.NoError(t, store.ActivateNodeRuntimeFence(ctx, node, "fence-new"))
	stale := old
	stale.ReportGeneration = 10
	stale.IdentityFingerprint = "identity-from-fenced-connection"
	err := store.UpsertNodeStatus(ctx, node, NodeStatusReport{
		NodeID:       node.NodeID,
		RuntimeFence: "fence-old",
		Agents: []AgentStatusInput{{
			AgentID: agent.AgentID, RuntimeIdentity: &stale,
		}},
	})
	require.ErrorIs(t, err, ErrConflict)

	fresh := old
	fresh.ReportEpoch = "fence-new"
	fresh.ReportGeneration = 1
	fresh.IdentityFingerprint = "identity-after-restart"
	fresh.PoolConsistency = "unknown"
	require.NoError(t, store.UpsertNodeStatus(ctx, node, NodeStatusReport{
		NodeID:       node.NodeID,
		RuntimeFence: "fence-new",
		Agents: []AgentStatusInput{{
			AgentID: agent.AgentID, RuntimeIdentity: &fresh,
		}},
	}))
	stored, err := store.GetAgentRuntimeIdentity(ctx, agent.AgentID)
	require.NoError(t, err)
	assert.Equal(t, "identity-after-restart", stored.IdentityFingerprint)
	assert.Equal(t, int64(1), stored.ReportGeneration)
	assert.Equal(t, "unknown", stored.PoolConsistency)
}

func TestPermissionChoiceGivenLegacyApprovalOverrideWhenStoredThenClearsChoiceAndPreservesCWD(
	t *testing.T,
) {
	ctx := t.Context()
	store, node, agent := sessionReportStoreFixture(t, ctx)
	principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
	_, err := store.CreateNodeAgentSession(ctx, principal, domain.CreateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "session-1",
		PaxConfig: domain.SessionPaxConfig{
			CWD:                "/workspace",
			ApprovalMode:       domain.SessionApprovalModeManual,
			PermissionChoiceID: "agent:mode:agent",
		},
	})
	require.NoError(t, err)

	require.NoError(t, store.SetSessionApprovalMode(
		ctx,
		agent.AgentID,
		"session-1",
		domain.SessionApprovalModeAutoApproveAll,
	))
	stored, err := store.GetSession(ctx, principal, "session-1")
	require.NoError(t, err)
	assert.Equal(t, "/workspace", stored.PaxConfig.CWD)
	assert.Equal(t, domain.SessionApprovalModeAutoApproveAll, stored.PaxConfig.ApprovalMode)
	assert.Empty(t, stored.PaxConfig.PermissionChoiceID)

	_, err = store.CreateNodeAgentSession(ctx, principal, domain.CreateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "session-2",
		PaxConfig: domain.SessionPaxConfig{
			CWD:                "/workspace-2",
			ApprovalMode:       domain.SessionApprovalModeManual,
			PermissionChoiceID: "agent:mode:read-only",
		},
	})
	require.NoError(t, err)
	updated, err := store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "session-2",
		PaxConfig: SessionPaxConfig{ApprovalMode: domain.SessionApprovalModeAutoApproveAll},
	})
	require.NoError(t, err)
	assert.Equal(t, "/workspace-2", updated.PaxConfig.CWD)
	assert.Equal(t, domain.SessionApprovalModeAutoApproveAll, updated.PaxConfig.ApprovalMode)
	assert.Empty(t, updated.PaxConfig.PermissionChoiceID)
	updated, err = store.UpdateNodeAgentSession(ctx, principal, UpdateSessionRequest{
		NodeID:    node.NodeID,
		AgentID:   agent.AgentID,
		SessionID: "session-2",
		PaxConfig: SessionPaxConfig{
			ApprovalMode:       domain.SessionApprovalModeManual,
			PermissionChoiceID: "agent:mode:read-only",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.SessionApprovalModeManual, updated.PaxConfig.ApprovalMode)
	assert.Equal(t, "agent:mode:read-only", updated.PaxConfig.PermissionChoiceID)
	require.ErrorIs(t, store.SetSessionApprovalMode(
		ctx,
		agent.AgentID,
		"missing-session",
		domain.SessionApprovalModeAutoApproveAll,
	), ErrNotFound)
}

func TestPostgresPermissionStoreGivenRowsWhenReadAndWrittenThenUsesTypedSchema(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	t.Run("given runtime identity then upserts fenced generation fields", func(t *testing.T) {
		script := &scriptedPostgresScript{execResults: []int64{1}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		err := store.UpsertAgentRuntimeIdentity(t.Context(), domain.AgentRuntimeIdentity{
			AgentID:             "agent-1",
			ReportEpoch:         "epoch-1",
			SchemaVersion:       2,
			ConnectionID:        "conn-1",
			ReportGeneration:    3,
			ACPAgentName:        "codex-acp",
			IdentityFingerprint: "identity-1",
			ObservedAt:          now,
		})
		require.NoError(t, err)
		require.Len(t, script.execTexts, 1)
		assert.Contains(t, script.execTexts[0], "INSERT INTO agent_runtime_identities")
		assert.Contains(t, script.execTexts[0], "report_generation <= EXCLUDED.report_generation")
		assert.Contains(t, script.execTexts[0], "report_epoch <> EXCLUDED.report_epoch")
	})

	t.Run("given provisional session then deletes the exact binding", func(t *testing.T) {
		script := &scriptedPostgresScript{execResults: []int64{1}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		err := store.DeleteProvisionalAgentSession(t.Context(), "agent-1", "session-1")
		require.NoError(t, err)
		require.Len(t, script.execTexts, 1)
		assert.Contains(t, script.execTexts[0], "DELETE FROM agent_sessions")
	})

	t.Run(
		"given authoritative snapshot identity then replaces within accepted fence",
		func(t *testing.T) {
			script := &scriptedPostgresScript{execResults: []int64{1}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			err := replaceAgentRuntimeIdentity(
				t.Context(),
				dbExecer{store.db},
				domain.AgentRuntimeIdentity{
					AgentID:             "agent-1",
					ReportEpoch:         "fence-1",
					ConnectionID:        "durable-connection",
					ReportGeneration:    1,
					IdentityFingerprint: "identity-after-restart",
					ObservedAt:          now,
				},
			)
			require.NoError(t, err)
			require.Len(t, script.execTexts, 1)
			assert.NotContains(t, script.execTexts[0], "WHERE agent_runtime_identities")
		},
	)

	t.Run("given runtime identity row then scans all typed values", func(t *testing.T) {
		script := &scriptedPostgresScript{queries: []scriptedRows{{
			columns: []string{
				"agent_id", "report_epoch", "schema_version", "connection_id", "report_generation",
				"protocol_version", "acp_agent_name", "acp_agent_title", "acp_agent_version",
				"runtime_name", "runtime_version", "runtime_build", "runtime_channel",
				"identity_fingerprint", "command_fingerprint", "client_profile_hash", "worker_result_hash",
				"pool_consistency", "observed_at",
			},
			values: [][]driver.Value{{
				"agent-1", "epoch-1", int64(2), "conn-1", int64(3), int64(1), "codex-acp", "Codex",
				"1.1.7", "codex", "0.58.0", "build", "stable", "identity-1",
				"command-1", "profile-1", "worker-1", "consistent", now,
			}},
		}}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		identity, err := store.GetAgentRuntimeIdentity(t.Context(), "agent-1")
		require.NoError(t, err)
		assert.Equal(t, "codex", identity.RuntimeName)
		assert.Equal(t, "identity-1", identity.IdentityFingerprint)
		assert.Equal(t, "profile-1", identity.ClientProfileHash)
	})

	t.Run("given profile insert conflict then preserves append-only revision", func(t *testing.T) {
		profile := domain.BuiltInCodexPermissionProfile(now)
		script := &scriptedPostgresScript{execResults: []int64{0}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		err := store.InsertPermissionProfile(t.Context(), profile)
		assert.ErrorIs(t, err, ErrConflict)
		assert.Contains(t, script.execTexts[0], "ON CONFLICT (profile_id, revision) DO NOTHING")
	})

	t.Run("given new profile revision then inserts it", func(t *testing.T) {
		profile := domain.BuiltInCodexPermissionProfile(now)
		profile.Revision = 2
		script := &scriptedPostgresScript{execResults: []int64{1}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		require.NoError(t, store.InsertPermissionProfile(t.Context(), profile))
		assert.Contains(t, script.execTexts[0], "INSERT INTO agent_permission_profiles")
	})

	t.Run("given active profile rows then decodes definition", func(t *testing.T) {
		definition, err := json.Marshal(domain.BuiltInCodexPermissionProfile(now).Definition)
		require.NoError(t, err)
		script := &scriptedPostgresScript{queries: []scriptedRows{{
			columns: []string{
				"profile_id", "revision", "status", "owner_user_id", "agent_type",
				"acp_agent_name", "acp_agent_version_constraint", "runtime_name",
				"runtime_version_constraint", "priority", "definition", "source", "created_at",
			},
			values: [][]driver.Value{{
				"codex-acp.permissions", int64(1), "active", nil, "codex",
				"codex-acp", "*", "codex", "*", int64(100), definition, "builtin", now,
			}},
		}}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		profiles, err := store.ListActivePermissionProfiles(t.Context(), "user-1", "codex")
		require.NoError(t, err)
		require.Len(t, profiles, 1)
		assert.Equal(t, "agent:mode:agent", profiles[0].Definition.DefaultChoiceID)
	})

	t.Run("given observation then upserts and reads revisioned catalog", func(t *testing.T) {
		catalog := domain.ObservedPermissionCatalog{
			Binding: domain.PermissionBinding{
				Kind:     domain.PermissionBindingConfigOption,
				ConfigID: "mode",
			},
			Options: []domain.ObservedPermissionOption{{Value: "agent", Name: "Agent"}},
		}
		raw, err := json.Marshal(catalog)
		require.NoError(t, err)
		script := &scriptedPostgresScript{queries: []scriptedRows{
			{columns: []string{"catalog_revision"}, values: [][]driver.Value{{int64(4)}}},
			{
				columns: []string{
					"agent_id", "identity_fingerprint", "catalog_revision", "catalog_hash",
					"catalog", "observed_at", "expires_at",
				},
				values: [][]driver.Value{{
					"agent-1", "identity-1", int64(4), "hash-1", raw, now, now.Add(time.Hour),
				}},
			}}}
		store, cleanup := scriptedPostgresStore(t, script)
		defer cleanup()
		stored, err := store.UpsertPermissionObservation(
			t.Context(),
			domain.AgentPermissionObservation{
				AgentID: "agent-1", IdentityFingerprint: "identity-1", CatalogHash: "hash-1",
				Catalog: catalog, ObservedAt: now, ExpiresAt: now.Add(time.Hour),
			},
		)
		require.NoError(t, err)
		assert.Equal(t, int64(4), stored.CatalogRevision)
		stored, err = store.GetPermissionObservation(t.Context(), "agent-1", "identity-1")
		require.NoError(t, err)
		assert.Equal(t, "agent", stored.Catalog.Options[0].Value)
	})

	t.Run(
		"given legacy approval override then preserves config and clears typed choice",
		func(t *testing.T) {
			script := &scriptedPostgresScript{execResults: []int64{1}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			err := store.SetSessionApprovalMode(
				t.Context(),
				"agent-1",
				"session-1",
				domain.SessionApprovalModeAutoApproveAll,
			)
			require.NoError(t, err)
			require.Len(t, script.execTexts, 1)
			assert.Contains(t, script.execTexts[0], "metadata->'pax_config'")
			assert.Contains(t, script.execTexts[0], "- 'permission_choice_id'")
			require.Len(t, script.execArgs, 1)
			assert.Equal(t, domain.SessionApprovalModeAutoApproveAll, script.execArgs[0][2].Value)
		},
	)
}

func TestPermissionSchemaGivenDatabaseBootstrapWhenReadThenContainsSeparateIndexedTables(
	t *testing.T,
) {
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	sql := string(initSQL)
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS agent_runtime_identities")
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS agent_permission_profiles")
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS agent_permission_observations")
	assert.Contains(t, sql, "PRIMARY KEY (profile_id, revision)")
	assert.Contains(t, sql, "PRIMARY KEY (agent_id, identity_fingerprint)")
	assert.Contains(t, sql, "ON CONFLICT (profile_id, revision) DO NOTHING")
	assert.Contains(t, sql, "idx_agent_permission_profiles_match")
}

package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRuntimeIdentityGivenCapabilityReportsWhenParsedThenSupportsV1AndPartialV2(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	t.Run("given v1 report then derives stable identity", func(t *testing.T) {
		raw := json.RawMessage(`{
			"schema_version":1,
			"connection_id":"conn-1",
			"report_generation":7,
			"protocol_version":1,
			"paxd_version":"0.9.0",
			"command_fingerprint":"command-hash",
			"worker_result_hash":"worker-hash"
		}`)
		identity, err := parseAgentRuntimeIdentity("agent-1", "codex", "conn-1", raw, observedAt)
		require.NoError(t, err)
		require.NotNil(t, identity)
		assert.Equal(t, 1, identity.SchemaVersion)
		assert.Equal(t, int64(7), identity.ReportGeneration)
		assert.Contains(t, identity.IdentityFingerprint, "sha256:")
		assert.Empty(t, identity.ACPAgentName)
	})

	t.Run("given full v2 report then reads adapter and runtime versions", func(t *testing.T) {
		raw := json.RawMessage(`{
			"schema_version":2,
			"connection_id":"conn-2",
			"report_generation":8,
			"protocol_version":1,
			"pool_consistency":"consistent",
			"implementation":{
				"acp_agent":{"name":"@agentclientprotocol/codex-acp","title":"Codex","version":"1.1.7"},
				"runtime":{"name":"codex","version":"0.58.0","build":"abc","channel":"stable"},
				"identity_fingerprint":"identity-2"
			}
		}`)
		identity, err := parseAgentRuntimeIdentity("agent-2", "codex", "conn-2", raw, observedAt)
		require.NoError(t, err)
		require.NotNil(t, identity)
		assert.Equal(t, "@agentclientprotocol/codex-acp", identity.ACPAgentName)
		assert.Equal(t, "1.1.7", identity.ACPAgentVersion)
		assert.Equal(t, "codex", identity.RuntimeName)
		assert.Equal(t, "0.58.0", identity.RuntimeVersion)
		assert.Contains(t, identity.IdentityFingerprint, "sha256:")
		assert.NotEqual(t, "identity-2", identity.IdentityFingerprint)
		assert.Equal(t, "consistent", identity.PoolConsistency)
	})

	t.Run(
		"given runtime-only v2 then accepts it with a conservative fingerprint",
		func(t *testing.T) {
			raw := json.RawMessage(`{
			"schema_version":2,
			"connection_id":"conn-3",
			"report_generation":9,
			"implementation":{"runtime":{"name":"codex","version":"0.59.0"}}
		}`)
			identity, err := parseAgentRuntimeIdentity(
				"agent-3",
				"codex",
				"conn-3",
				raw,
				observedAt,
			)
			require.NoError(t, err)
			require.NotNil(t, identity)
			assert.Empty(t, identity.ACPAgentName)
			assert.Equal(t, "codex", identity.RuntimeName)
			assert.Contains(t, identity.IdentityFingerprint, "sha256:")
		},
	)

	t.Run(
		"given an unknown schema then identity is rejected without panicking",
		func(t *testing.T) {
			identity, err := parseAgentRuntimeIdentity(
				"agent-4",
				"codex",
				"conn-4",
				json.RawMessage(`{"schema_version":99}`),
				observedAt,
			)
			assert.Error(t, err)
			assert.Nil(t, identity)
		},
	)
}

func TestRuntimeIdentityGivenMissingOrInvalidReportWhenSnapshotAppliedThenDowngradesPriorIdentity(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	principal := testUserPrincipal(t, srv, "owner@example.com")
	node, err := srv.store.GetNode(t.Context(), principal, fixture.nodeID)
	require.NoError(t, err)
	runtimeStore, ok := srv.store.(domain.SessionRuntimeSnapshotStore)
	require.True(t, ok)
	require.NoError(t, runtimeStore.ActivateNodeRuntimeFence(t.Context(), node, "fence-1"))
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	srv.clock = func() time.Time { return now }
	valid := json.RawMessage(`{
		"schema_version":2,"connection_id":"durable-connection","report_generation":9,
		"protocol_version":1,"pool_consistency":"consistent",
		"implementation":{"acp_agent":{"name":"@agentclientprotocol/codex-acp","version":"1.1.7"},
		"runtime":{"name":"codex","version":"0.58.0"},"identity_fingerprint":"identity-valid"}
	}`)
	apply := func(raw json.RawMessage) {
		require.NoError(t, srv.upsertRuntimeSnapshotReport(
			t.Context(),
			node,
			"fence-1",
			nodeControlReport{RuntimeSnapshot: &nodeControlRuntimeSnapshot{
				SnapshotID: "snapshot",
				Agents: []nodeControlAgentRuntime{{
					ConnectionID:            "durable-connection",
					CloudAgentID:            fixture.agentID,
					AgentType:               "codex",
					RuntimePhase:            "running",
					ObservedGeneration:      1,
					ACPPoolCapabilityReport: raw,
				}},
			}},
		))
	}

	for _, test := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "missing report", raw: nil},
		{name: "unsupported report schema", raw: json.RawMessage(`{"schema_version":99}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			apply(valid)
			before, err := srv.store.GetAgentRuntimeIdentity(t.Context(), fixture.agentID)
			require.NoError(t, err)
			assert.Equal(t, "consistent", before.PoolConsistency)

			apply(test.raw)
			after, err := srv.store.GetAgentRuntimeIdentity(t.Context(), fixture.agentID)
			require.NoError(t, err)
			assert.Equal(t, "unknown", after.PoolConsistency)
			assert.NotEqual(t, before.IdentityFingerprint, after.IdentityFingerprint)
			assert.Equal(t, "fence-1", after.ReportEpoch)
		})
	}
}

func TestPermissionCatalogGivenProfileAndObservationsWhenResolvedThenUsesSafePriority(
	t *testing.T,
) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(t.Context(), owner, domain.RegisterAgentRequest{
		Name: "codex", AgentType: "codex", Hostname: "host", OS: "linux",
	}, "agent-key")
	require.NoError(t, err)
	srv := &Service{store: newCanonicalSessionStore(store), clock: func() time.Time { return now }}

	identity := domain.AgentRuntimeIdentity{
		AgentID:             agent.AgentID,
		SchemaVersion:       2,
		ConnectionID:        "conn-1",
		ReportGeneration:    1,
		ACPAgentName:        "@agentclientprotocol/codex-acp",
		ACPAgentVersion:     "1.1.7",
		RuntimeName:         "codex",
		RuntimeVersion:      "0.58.0",
		IdentityFingerprint: "identity-1",
		PoolConsistency:     "consistent",
		ObservedAt:          now,
	}
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))

	t.Run("given no observation then returns the matching profile", func(t *testing.T) {
		catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
		require.NoError(t, err)
		assert.Equal(t, domain.PermissionCatalogSourceProfile, catalog.Source)
		assert.False(t, catalog.Stale)
		assert.Equal(t, "agent:mode:agent", catalog.DefaultChoiceID)
		assert.Len(t, catalog.Choices, 4)
	})

	observation := domain.AgentPermissionObservation{
		AgentID:             agent.AgentID,
		IdentityFingerprint: identity.IdentityFingerprint,
		CatalogHash:         "catalog-1",
		Catalog: domain.ObservedPermissionCatalog{
			Binding: domain.PermissionBinding{
				Kind:         domain.PermissionBindingConfigOption,
				ConfigID:     "mode",
				Category:     "mode",
				CurrentValue: "agent",
			},
			DefaultChoiceID: "agent:mode:agent",
			Options: []domain.ObservedPermissionOption{
				{Value: "read-only", Name: "Read-only live"},
				{Value: "agent", Name: "Agent live"},
				{Value: "dangerous-root", Name: "Dangerous root"},
			},
		},
		ObservedAt: now,
		ExpiresAt:  now.Add(time.Hour),
	}
	_, err = store.UpsertPermissionObservation(t.Context(), observation)
	require.NoError(t, err)

	t.Run("given a current observation then it overrides the profile", func(t *testing.T) {
		catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
		require.NoError(t, err)
		assert.Equal(t, domain.PermissionCatalogSourceObserved, catalog.Source)
		assert.Equal(t, "agent:mode:agent", catalog.DefaultChoiceID)
		assert.Len(t, catalog.Choices, 4)
		assert.Equal(t, "workspace_write", catalog.Choices[2].Risk)
		assert.Equal(t, "unknown", catalog.Choices[3].Risk)
		assert.True(t, catalog.Choices[3].RequiresConfirmation)
	})

	t.Run(
		"given an existing session choice then resolves the current observed binding",
		func(t *testing.T) {
			resolved, err := srv.resolveStoredPermissionChoice(
				t.Context(),
				agent,
				"agent:mode:read-only",
			)
			require.NoError(t, err)
			assert.Equal(t, domain.SessionApprovalModeManual, resolved.ApprovalMode)
			assert.Equal(t, domain.PermissionBindingConfigOption, resolved.Binding.Kind)
			assert.Equal(t, "mode", resolved.Binding.ConfigID)
			assert.Equal(t, "read-only", resolved.Value)
		},
	)

	t.Run(
		"given a current observation with a missing runtime version then marks it stale",
		func(t *testing.T) {
			identity.ReportGeneration++
			identity.RuntimeVersion = ""
			require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
			catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
			require.NoError(t, err)
			assert.Equal(t, domain.PermissionCatalogSourceObserved, catalog.Source)
			assert.True(t, catalog.Stale)
		},
	)

	t.Run("given unknown pool consistency then ignores slot observation", func(t *testing.T) {
		identity.ReportGeneration++
		identity.PoolConsistency = "unknown"
		require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
		catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
		require.NoError(t, err)
		assert.Equal(t, domain.PermissionCatalogSourceProfile, catalog.Source)
		assert.True(t, catalog.Stale)
	})

	t.Run("given a mixed pool then fails closed to pax only", func(t *testing.T) {
		identity.ReportGeneration++
		identity.PoolConsistency = "mixed"
		require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
		catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
		require.NoError(t, err)
		assert.Equal(t, domain.PermissionCatalogSourcePAXOnly, catalog.Source)
		require.Len(t, catalog.Choices, 1)
		assert.Equal(t, domain.PermissionChoicePAXAutoApprove, catalog.Choices[0].ChoiceID)
	})
}

func TestPermissionCatalogGivenUnprofiledObservationWhenMappedThenFailsClosed(t *testing.T) {
	catalog := catalogFromObservation(domain.AgentPermissionObservation{
		CatalogRevision: 7,
		Catalog: domain.ObservedPermissionCatalog{
			Binding: domain.PermissionBinding{
				Kind:         domain.PermissionBindingConfigOption,
				ConfigID:     "mode",
				CurrentValue: "dangerous-root",
			},
			DefaultChoiceID: "agent:mode:dangerous-root",
			Options: []domain.ObservedPermissionOption{{
				Value: "dangerous-root",
				Name:  "Dangerous root",
			}},
		},
	}, domain.AgentPermissionProfile{}, false)

	assert.Empty(t, catalog.DefaultChoiceID)
	require.Len(t, catalog.Choices, 2)
	assert.Equal(t, "unknown", catalog.Choices[1].Risk)
	assert.True(t, catalog.Choices[1].RequiresConfirmation)

	profile := domain.BuiltInCodexPermissionProfile(time.Now())
	mismatched := catalogFromObservation(domain.AgentPermissionObservation{
		CatalogRevision: 8,
		Catalog: domain.ObservedPermissionCatalog{
			Binding: domain.PermissionBinding{
				Kind:         domain.PermissionBindingConfigOption,
				ConfigID:     "permission-mode",
				Category:     "mode",
				CurrentValue: "agent",
			},
			Options: []domain.ObservedPermissionOption{{Value: "agent", Name: "Agent"}},
		},
	}, profile, true)
	assert.Empty(t, mismatched.DefaultChoiceID)
	require.Len(t, mismatched.Choices, 2)
	assert.Equal(t, "agent:config-option:permission-mode:agent", mismatched.Choices[1].ChoiceID)
	assert.Equal(t, "unknown", mismatched.Choices[1].Risk)
	assert.True(t, mismatched.Choices[1].RequiresConfirmation)
}

func TestPermissionCatalogGivenMalformedSelectedProfileWhenResolvedThenFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(t.Context(), owner, domain.RegisterAgentRequest{
		Name: "codex", AgentType: "codex", Hostname: "host", OS: "linux",
	}, "agent-key")
	require.NoError(t, err)
	malformed := domain.BuiltInCodexPermissionProfile(now)
	malformed.ProfileID = "owner-malformed"
	malformed.OwnerUserID = owner.UserID
	malformed.Priority = 1000
	malformed.Definition.NativeChoices[1].ChoiceID = domain.PermissionChoicePAXAutoApprove
	require.NoError(t, store.InsertPermissionProfile(t.Context(), malformed))
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), domain.AgentRuntimeIdentity{
		AgentID:             agent.AgentID,
		SchemaVersion:       2,
		ConnectionID:        "conn-1",
		ACPAgentName:        "@agentclientprotocol/codex-acp",
		ACPAgentVersion:     "1.1.7",
		RuntimeName:         "codex",
		RuntimeVersion:      "0.58.0",
		IdentityFingerprint: "identity-1",
		PoolConsistency:     "consistent",
		ObservedAt:          now,
	}))
	observation := domain.AgentPermissionObservation{
		AgentID:             agent.AgentID,
		IdentityFingerprint: "identity-1",
		CatalogHash:         "hash-1",
		Catalog: parseObservedPermissionCatalog(json.RawMessage(`{
			"configOptions":[{"id":"mode","category":"mode","currentValue":"agent",
			"options":[{"value":"agent","name":"Agent"}]}]
		}`)),
		ObservedAt: now,
		ExpiresAt:  now.Add(time.Hour),
	}
	_, err = store.UpsertPermissionObservation(t.Context(), observation)
	require.NoError(t, err)
	srv := &Service{store: newCanonicalSessionStore(store), clock: func() time.Time { return now }}

	_, err = srv.resolveAgentPermissionCatalog(t.Context(), agent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "native choices require an agent: choice_id")
	_, err = srv.resolveConversationPermissionChoice(
		t.Context(),
		domain.UserPrincipal{User: owner},
		agent.AgentID,
		"agent:mode:agent",
		json.RawMessage(
			`{"configOptions":[{"id":"mode","category":"mode","options":[{"value":"agent","name":"Agent"}]}]}`,
		),
	)
	var httpErr apperr.Error
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadGateway, httpErr.Status)
}

func TestPermissionObservationGivenSessionNewFrameWhenProjectedThenCachesConsistentPositiveAndNegativeCatalog(
	t *testing.T,
) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(t.Context(), owner, domain.RegisterAgentRequest{
		Name: "codex", AgentType: "codex", Hostname: "host", OS: "linux",
	}, "agent-key")
	require.NoError(t, err)
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), domain.AgentRuntimeIdentity{
		AgentID:             agent.AgentID,
		SchemaVersion:       2,
		ConnectionID:        "conn-1",
		ReportGeneration:    1,
		IdentityFingerprint: "identity-1",
		PoolConsistency:     "consistent",
		ObservedAt:          now,
	}))
	srv := &Service{store: newCanonicalSessionStore(store), clock: func() time.Time { return now }}
	result := json.RawMessage(`{
		"configOptions":[{
			"id":"mode",
			"category":"mode",
			"currentValue":"agent",
			"options":[{"value":"agent","name":"Agent"}]
		}]
	}`)
	nextCalled := false
	err = (permissionObservationMiddleware{service: srv}).HandleACPFrame(
		t.Context(),
		&acpFrameContext{
			agent:       &ACPTunnelAgent{agentID: agent.AgentID},
			direction:   acpAgentToUser,
			requestKind: "session/new",
			frame:       acpJSONRPCMessage{Result: result},
		},
		func(context.Context, *acpFrameContext) error {
			nextCalled = true
			return nil
		},
	)
	require.NoError(t, err)
	assert.True(t, nextCalled)
	observation, err := store.GetPermissionObservation(t.Context(), agent.AgentID, "identity-1")
	require.NoError(t, err)
	assert.Equal(t, "agent", observation.Catalog.Binding.CurrentValue)
	assert.Equal(t, int64(1), observation.CatalogRevision)

	require.NoError(t, srv.observePermissionCatalog(
		t.Context(),
		agent.AgentID,
		json.RawMessage(`{"configOptions":[{"id":"model","options":[]}]}`),
	))
	negative, err := store.GetPermissionObservation(t.Context(), agent.AgentID, "identity-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), negative.CatalogRevision)
	assert.Empty(t, negative.Catalog.Binding.Kind)
	assert.Empty(t, negative.Catalog.Options)
	negativeCatalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
	require.NoError(t, err)
	assert.Equal(t, domain.PermissionCatalogSourceObserved, negativeCatalog.Source)
	require.Len(t, negativeCatalog.Choices, 1)
	assert.Equal(t, domain.PermissionChoicePAXAutoApprove, negativeCatalog.Choices[0].ChoiceID)

	identity, err := store.GetAgentRuntimeIdentity(t.Context(), agent.AgentID)
	require.NoError(t, err)
	identity.ReportGeneration++
	identity.PoolConsistency = "unknown"
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
	require.NoError(t, srv.observePermissionCatalog(t.Context(), agent.AgentID, json.RawMessage(`{
		"configOptions":[{
			"id":"mode","category":"mode",
			"options":[{"value":"read-only","name":"Read-only"}]
		}]
	}`)))
	unchanged, err := store.GetPermissionObservation(t.Context(), agent.AgentID, "identity-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), unchanged.CatalogRevision)
}

func TestPermissionConversationGivenChoiceWhenResolvedThenRequiresLiveSafeMatch(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(t.Context(), owner, domain.RegisterAgentRequest{
		Name: "codex", AgentType: "codex", Hostname: "host", OS: "linux",
	}, "agent-key")
	require.NoError(t, err)
	principal := domain.UserPrincipal{User: owner}
	srv := &Service{store: newCanonicalSessionStore(store), clock: func() time.Time { return now }}
	live := json.RawMessage(`{
		"configOptions":[{
			"id":"mode",
			"category":"mode",
			"options":[{"value":"agent","name":"Agent"}]
		}]
	}`)

	t.Run("given pax auto then no live options are required", func(t *testing.T) {
		resolved, err := srv.resolveConversationPermissionChoice(
			t.Context(), principal, agent.AgentID,
			domain.PermissionChoicePAXAutoApprove, nil,
		)
		require.NoError(t, err)
		assert.Equal(t, domain.SessionApprovalModeAutoApproveAll, resolved.ApprovalMode)
	})

	t.Run("given native choice then exact live value forces manual", func(t *testing.T) {
		resolved, err := srv.resolveConversationPermissionChoice(
			t.Context(), principal, agent.AgentID, "agent:mode:agent", live,
		)
		require.NoError(t, err)
		assert.Equal(t, domain.SessionApprovalModeManual, resolved.ApprovalMode)
		assert.Equal(t, "agent", resolved.Value)
	})

	t.Run("given no live permission options then returns bad gateway", func(t *testing.T) {
		_, err := srv.resolveConversationPermissionChoice(
			t.Context(), principal, agent.AgentID, "agent:mode:agent", json.RawMessage(`{}`),
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadGateway, httpErr.Status)
	})

	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), domain.AgentRuntimeIdentity{
		AgentID:             agent.AgentID,
		SchemaVersion:       2,
		ConnectionID:        "conn-1",
		ReportGeneration:    1,
		IdentityFingerprint: "identity-1",
		PoolConsistency:     "mixed",
		ObservedAt:          now,
	}))
	t.Run("given mixed worker pool then rejects native choice", func(t *testing.T) {
		_, err := srv.resolveConversationPermissionChoice(
			t.Context(), principal, agent.AgentID, "agent:mode:agent", live,
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusConflict, httpErr.Status)
	})
}

func TestPermissionProfileGivenVersionConstraintsWhenMatchedThenUnknownIsStaleAndMismatchRejected(
	t *testing.T,
) {
	profile := domain.BuiltInCodexPermissionProfile(time.Now())
	profile.ACPAgentVersionConstraint = ">=1.1.0 <2.0.0"
	profile.RuntimeVersionConstraint = ">=0.50.0 <1.0.0"

	matched, uncertain := permissionProfileMatches(profile, domain.AgentRuntimeIdentity{
		ACPAgentName:    "@agentclientprotocol/codex-acp",
		ACPAgentVersion: "1.2.0",
		RuntimeName:     "codex",
		RuntimeVersion:  "0.58.0",
	}, true)
	assert.True(t, matched)
	assert.False(t, uncertain)

	matched, uncertain = permissionProfileMatches(profile, domain.AgentRuntimeIdentity{
		ACPAgentName: "@agentclientprotocol/codex-acp",
	}, true)
	assert.True(t, matched)
	assert.True(t, uncertain)

	matched, uncertain = permissionProfileMatches(profile, domain.AgentRuntimeIdentity{
		ACPAgentName:    "@agentclientprotocol/codex-acp",
		ACPAgentVersion: "2.0.0",
		RuntimeName:     "codex",
		RuntimeVersion:  "0.58.0",
	}, true)
	assert.False(t, matched)
	assert.False(t, uncertain)

	matched, uncertain = permissionVersionMatches("not a constraint", "1.0.0")
	assert.False(t, matched)
	assert.False(t, uncertain)
}

func TestConversationRequestGivenPermissionChoiceAndLegacyApprovalWhenValidatedThenRequiresConsistency(
	t *testing.T,
) {
	tests := []struct {
		name     string
		choiceID string
		approval string
		wantErr  string
	}{
		{
			name:     "given native and manual then valid",
			choiceID: "agent:mode:agent",
			approval: domain.SessionApprovalModeManual,
		},
		{
			name:     "given pax auto and auto approval then valid",
			choiceID: domain.PermissionChoicePAXAutoApprove,
			approval: domain.SessionApprovalModeAutoApproveAll,
		},
		{
			name:     "given native and auto approval then conflict",
			choiceID: "agent:mode:agent",
			approval: domain.SessionApprovalModeAutoApproveAll,
			wantErr:  "approval_mode conflicts with permission_choice_id",
		},
		{
			name:     "given pax auto and manual then conflict",
			choiceID: domain.PermissionChoicePAXAutoApprove,
			approval: domain.SessionApprovalModeManual,
			wantErr:  "approval_mode conflicts with permission_choice_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateConversationSessionOptions(conversationRequest{
				PermissionChoiceID: test.choiceID,
				ApprovalMode:       test.approval,
			})
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.wantErr)
		})
	}
}

func TestPermissionChoiceGivenLiveSessionOptionsWhenResolvedThenEnforcesServerPolicy(t *testing.T) {
	live := parseObservedPermissionCatalog(json.RawMessage(`{
		"configOptions":[{
			"id":"mode",
			"category":"mode",
			"currentValue":"agent",
			"options":[
				{"value":"read-only","name":"Read-only"},
				{"value":"agent","name":"Agent"}
			]
		}]
	}`))
	profile := domain.BuiltInCodexPermissionProfile(time.Now())

	t.Run("given pax auto then forces auto approve without agent config", func(t *testing.T) {
		resolved, err := resolvePermissionChoiceFromLive(
			domain.PermissionChoicePAXAutoApprove,
			domain.ObservedPermissionCatalog{},
			profile,
			true,
		)
		require.NoError(t, err)
		assert.Equal(t, domain.SessionApprovalModeAutoApproveAll, resolved.ApprovalMode)
		assert.Empty(t, resolved.Value)
	})

	t.Run("given a live native choice then forces manual and exact value", func(t *testing.T) {
		resolved, err := resolvePermissionChoiceFromLive(
			"agent:mode:agent",
			live,
			profile,
			true,
		)
		require.NoError(t, err)
		assert.Equal(t, domain.SessionApprovalModeManual, resolved.ApprovalMode)
		assert.Equal(t, "mode", resolved.Binding.ConfigID)
		assert.Equal(t, "agent", resolved.Value)
	})

	t.Run("given a cached choice missing live then returns conflict", func(t *testing.T) {
		_, err := resolvePermissionChoiceFromLive(
			"agent:mode:agent-full-access",
			live,
			profile,
			true,
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusConflict, httpErr.Status)
	})

	t.Run("given a changed binding then rejects the stale profile choice id", func(t *testing.T) {
		changedBinding := live
		changedBinding.Binding.ConfigID = "permission-mode"
		_, err := resolvePermissionChoiceFromLive(
			"agent:mode:agent",
			changedBinding,
			profile,
			true,
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusConflict, httpErr.Status)

		resolved, err := resolvePermissionChoiceFromLive(
			"agent:config-option:permission-mode:agent",
			changedBinding,
			profile,
			true,
		)
		require.NoError(t, err)
		assert.Equal(t, "permission-mode", resolved.Binding.ConfigID)
	})
}

func TestPermissionBindingGivenProfileAndLiveWhenComparedThenRequiresDeclaredFields(t *testing.T) {
	profile := domain.PermissionBinding{
		Kind: domain.PermissionBindingConfigOption, ConfigID: "mode", Category: "mode",
	}
	assert.True(t, permissionBindingsCompatible(profile, profile))
	changedCategory := profile
	changedCategory.Category = "security"
	assert.False(t, permissionBindingsCompatible(profile, changedCategory))
	assert.False(t, permissionBindingsCompatible(
		profile,
		domain.PermissionBinding{Kind: domain.PermissionBindingLegacyMode, ConfigID: "mode"},
	))
}

func TestPermissionObservationGivenNonModeConfigOptionsWhenModesExistThenUsesLegacyModes(
	t *testing.T,
) {
	catalog := parseObservedPermissionCatalog(json.RawMessage(`{
		"configOptions":[{
			"id":"model",
			"category":"model",
			"currentValue":"model-a",
			"options":[{"value":"model-a","name":"Model A"}]
		}],
		"modes":{
			"currentModeId":"agent",
			"availableModes":[{"id":"agent","name":"Agent"}]
		}
	}`))
	assert.Equal(t, domain.PermissionBindingLegacyMode, catalog.Binding.Kind)
	assert.Equal(t, "agent:legacy-mode:agent", catalog.DefaultChoiceID)
	require.Len(t, catalog.Options, 1)
	assert.Equal(t, "agent", catalog.Options[0].Value)
}

func TestPermissionObservationGivenUnrelatedNonStringConfigWhenParsedThenKeepsMode(t *testing.T) {
	catalog := parseObservedPermissionCatalog(json.RawMessage(`{
		"configOptions":[
			{"id":"telemetry","category":"other","currentValue":true,
			 "options":[{"value":42,"name":{"unexpected":true}}]},
			{"id":"mode","category":"mode","currentValue":"agent",
			 "options":[{"value":"agent","name":"Agent"}]}
		]
	}`))

	assert.Equal(t, domain.PermissionBindingConfigOption, catalog.Binding.Kind)
	assert.Equal(t, "agent:config-option:mode:agent", catalog.DefaultChoiceID)
	require.Len(t, catalog.Options, 1)
	assert.Equal(t, "agent", catalog.Options[0].Value)
}

func TestPermissionProfileGivenConfirmationRequiredChoiceWhenDefaultedThenRejectsProfile(
	t *testing.T,
) {
	profile := domain.BuiltInCodexPermissionProfile(time.Now())
	profile.Definition.DefaultChoiceID = "agent:mode:agent-full-access"
	err := validatePermissionProfileDefinition(profile.Definition)
	assert.EqualError(t, err, "default_choice_id cannot require confirmation")
}

func TestPermissionProfileGivenInvalidDeclarativeDefinitionWhenValidatedThenRejectsIt(
	t *testing.T,
) {
	base := domain.BuiltInCodexPermissionProfile(time.Now()).Definition
	tests := []struct {
		name   string
		mutate func(*domain.PermissionProfileDefinition)
		want   string
	}{
		{
			name: "unsupported binding",
			mutate: func(definition *domain.PermissionProfileDefinition) {
				definition.Binding.Kind = "rpc"
			},
			want: `unsupported binding kind "rpc"`,
		},
		{
			name: "missing config id",
			mutate: func(definition *domain.PermissionProfileDefinition) {
				definition.Binding.ConfigID = ""
			},
			want: "config_option binding requires config_id",
		},
		{
			name: "duplicate native value",
			mutate: func(definition *domain.PermissionProfileDefinition) {
				definition.NativeChoices[1].Value = definition.NativeChoices[0].Value
			},
			want: "native choice ids and values must be unique",
		},
		{
			name: "unknown default",
			mutate: func(definition *domain.PermissionProfileDefinition) {
				definition.DefaultChoiceID = "agent:mode:missing"
			},
			want: "default_choice_id must reference a native choice",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := domain.ClonePermissionProfileDefinition(base)
			test.mutate(&definition)
			assert.EqualError(t, validatePermissionProfileDefinition(definition), test.want)
		})
	}
}

func TestPermissionObservationGivenConfirmationRequiredCurrentModeWhenResolvedThenHasNoDefault(
	t *testing.T,
) {
	profile := domain.BuiltInCodexPermissionProfile(time.Now())
	catalog := catalogFromObservation(domain.AgentPermissionObservation{
		CatalogRevision: 1,
		Catalog: domain.ObservedPermissionCatalog{
			Binding: domain.PermissionBinding{
				Kind:         domain.PermissionBindingConfigOption,
				ConfigID:     "mode",
				CurrentValue: "agent-full-access",
			},
			DefaultChoiceID: "agent:mode:agent-full-access",
			Options: []domain.ObservedPermissionOption{
				{Value: "agent-full-access", Name: "Agent (full access)"},
			},
		},
	}, profile, true)
	assert.Empty(t, catalog.DefaultChoiceID)
	require.Len(t, catalog.Choices, 2)
	assert.True(t, catalog.Choices[1].RequiresConfirmation)
}

type recordingPermissionRequester struct {
	method string
	params map[string]any
	err    error
}

func (r *recordingPermissionRequester) request(
	_ context.Context,
	method string,
	params map[string]any,
	_ <-chan struct{},
) (conversationResponse, error) {
	r.method = method
	r.params = params
	return conversationResponse{}, r.err
}

func TestPermissionChoiceGivenResolvedNativeConfigWhenAppliedThenUsesExpectedACPMethod(
	t *testing.T,
) {
	t.Run("given config option then sends set config option", func(t *testing.T) {
		requester := &recordingPermissionRequester{}
		err := applyResolvedPermissionChoice(
			t.Context(),
			requester,
			"session-1",
			domain.ResolvedPermissionChoice{
				Binding: domain.PermissionBinding{
					Kind:     domain.PermissionBindingConfigOption,
					ConfigID: "mode",
				},
				Value: "agent",
			},
		)
		require.NoError(t, err)
		assert.Equal(t, "session/set_config_option", requester.method)
		assert.Equal(t, "session-1", requester.params["sessionId"])
		assert.Equal(t, "mode", requester.params["configId"])
		assert.Equal(t, "agent", requester.params["value"])
	})

	t.Run("given legacy mode then sends set mode", func(t *testing.T) {
		requester := &recordingPermissionRequester{}
		err := applyResolvedPermissionChoice(
			t.Context(),
			requester,
			"session-1",
			domain.ResolvedPermissionChoice{
				Binding: domain.PermissionBinding{Kind: domain.PermissionBindingLegacyMode},
				Value:   "auto",
			},
		)
		require.NoError(t, err)
		assert.Equal(t, "session/set_mode", requester.method)
		assert.Equal(t, "auto", requester.params["modeId"])
	})

	t.Run("given ACP failure then propagates and does not pretend success", func(t *testing.T) {
		wantErr := errors.New("set config failed")
		requester := &recordingPermissionRequester{err: wantErr}
		err := applyResolvedPermissionChoice(
			t.Context(),
			requester,
			"session-1",
			domain.ResolvedPermissionChoice{
				Binding: domain.PermissionBinding{
					Kind:     domain.PermissionBindingConfigOption,
					ConfigID: "mode",
				},
				Value: "agent",
			},
		)
		assert.ErrorIs(t, err, wantErr)
	})

	t.Run("given pax-only resolution then sends no agent RPC", func(t *testing.T) {
		requester := &recordingPermissionRequester{}
		err := applyResolvedPermissionChoice(
			t.Context(),
			requester,
			"session-1",
			domain.ResolvedPermissionChoice{},
		)
		require.NoError(t, err)
		assert.Empty(t, requester.method)
	})

	t.Run("given unsupported binding then returns bad gateway", func(t *testing.T) {
		requester := &recordingPermissionRequester{}
		err := applyResolvedPermissionChoice(
			t.Context(),
			requester,
			"session-1",
			domain.ResolvedPermissionChoice{
				Binding: domain.PermissionBinding{Kind: "unknown"},
				Value:   "agent",
			},
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadGateway, httpErr.Status)
		assert.Empty(t, requester.method)
	})
}

func TestPermissionChoiceGivenPostCreateFailureWhenRolledBackThenClosesAndDeletesProvisionalSession(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err := srv.store.CreateNodeAgentSession(t.Context(), principal, domain.CreateSessionRequest{
		NodeID:    fixture.nodeID,
		AgentID:   fixture.agentID,
		SessionID: "provisional-session",
		NativeID:  "native-provisional-session",
		Source:    domain.MessageSourceACPTunnel,
	})
	require.NoError(t, err)

	requester := &recordingPermissionRequester{err: errors.New("close unsupported")}
	srv.rollbackProvisionalConversationSession(
		t.Context(),
		requester,
		fixture.agentID,
		"provisional-session",
	)

	assert.Equal(t, "session/close", requester.method)
	assert.Equal(t, "provisional-session", requester.params["sessionId"])
	_, err = srv.store.GetSession(t.Context(), principal, "provisional-session")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPermissionCatalogEndpointGivenOwnerScopeWhenRequestedThenHidesForeignAgents(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	agentID := testAgentID(t, srv, "owner@example.com")

	t.Run("given owner then returns frontend catalog contract", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/agents/"+agentID+"/permission-catalog",
			nil,
		)
		req.Header.Set("X-User-Email", "owner@example.com")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		catalog := decodeData[domain.AgentPermissionCatalog](t, rec.Body.Bytes())
		assert.Equal(t, domain.PermissionCatalogSourcePAXOnly, catalog.Source)
		require.NotEmpty(t, catalog.Choices)
		assert.Equal(t, domain.PermissionChoiceKindPAX, catalog.Choices[0].Kind)
	})

	t.Run("given another user route then returns not found", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/other-user/agents/"+agentID+"/permission-catalog",
			nil,
		)
		req.Header.Set("X-User-Email", "owner@example.com")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

	t.Run("given unknown owned agent then returns not found", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/agents/missing/permission-catalog",
			nil,
		)
		req.Header.Set("X-User-Email", "owner@example.com")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})

}

func TestPermissionCatalogGivenOpenAPIDocumentWhenGeneratedThenEndpointIsDocumented(t *testing.T) {
	raw, err := openAPIDocument("https://manager.example")
	require.NoError(t, err)
	var document struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &document))
	assert.Contains(t, document.Paths, openAPIUserAgentPermissionCatalog)
}

func TestConversationGivenNativePermissionChoiceWhenCreatingThenConfiguresBeforePrompt(
	t *testing.T,
) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleConversation)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	defer httpServer.CloseClientConnections()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+fixture.agentID,
		http.Header{"X-Pax-Key": []string{fixture.nodeAPIKey}},
	)
	require.NoError(t, err)
	defer func() { _ = agentWS.Close() }()
	completeMockAgentReconcile(t, agentWS, fixture.agentID, 1)
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")
	defer func() {
		if !t.Failed() {
			return
		}
		conn, findErr := srv.acpTunnels.findAny(fixture.agentID, "")
		if findErr != nil {
			t.Logf("failed test tunnel: %v", findErr)
			return
		}
		t.Logf("producer stats: %+v", conn.reliableEngine.Producer.Stats())
		frames, replayErr := conn.reliableEngine.Store.ListInboundReplay(
			t.Context(),
			conn.queueID(),
			"acp",
			10,
		)
		t.Logf("pending inbound frames: %+v; error: %v", frames, replayErr)
		t.Logf("tunnel state: %s", srv.acpTunnels.debugSnapshot(fixture.agentID))
	}()

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go postConversation(
		t,
		httpServer.URL,
		fixture,
		`{"input":"hello","permission_choice_id":"agent:config-option:mode:agent","approval_mode":"manual"}`,
		respCh,
		errCh,
	)

	sessionNew, sessionNewRequest := readMockACPRequest(t, agentWS, "session/new")
	managerSessionID := sessionNew.Metadata["manager_session_id"]
	require.NotEmpty(t, managerSessionID)
	writeMockACPResponse(
		t,
		agentWS,
		sessionNew.QueueID,
		1,
		sessionNewRequest.ID,
		json.RawMessage(`{
				"sessionId":"native-permission-session",
				"configOptions":[{
					"id":"mode",
					"category":"mode",
					"currentValue":"read-only",
					"options":[
						{"value":"read-only","name":"Read-only"},
						{"value":"agent","name":"Agent"}
					]
				}]
		}`),
	)

	setConfig, setConfigRequest := readMockACPRequest(
		t,
		agentWS,
		"session/set_config_option",
	)
	assert.Equal(t, managerSessionID, setConfig.Metadata["manager_session_id"])
	assert.Equal(t, "native-permission-session", setConfig.Metadata["native_session_id"])
	assertACPParamString(t, setConfig.Payload, "sessionId", managerSessionID)
	assertACPParamString(t, setConfig.Payload, "configId", "mode")
	assertACPParamString(t, setConfig.Payload, "value", "agent")
	writeMockACPResponse(
		t,
		agentWS,
		setConfig.QueueID,
		2,
		setConfigRequest.ID,
		json.RawMessage(`{}`),
	)

	prompt, promptRequest := readMockACPRequest(t, agentWS, "session/prompt")
	assert.Equal(t, managerSessionID, prompt.Metadata["manager_session_id"])
	assert.Equal(t, "native-permission-session", prompt.Metadata["native_session_id"])
	assertFrameSessionID(t, prompt.Payload, managerSessionID)
	writeMockACPResponse(
		t,
		agentWS,
		prompt.QueueID,
		3,
		promptRequest.ID,
		json.RawMessage(`{"stopReason":"end_turn"}`),
	)

	body := readConversationResponse(t, respCh, errCh, http.StatusOK)
	events := decodeConversationEvents(t, body)
	sessionEvent := requireConversationEvent(t, events, "session")
	stored, err := srv.store.GetSession(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		sessionEvent.SessionID,
	)
	require.NoError(t, err)
	assert.Equal(t, "native-permission-session", stored.NativeID)
	assert.Equal(t, domain.SessionApprovalModeManual, stored.PaxConfig.ApprovalMode)
	assert.Equal(t, "agent:config-option:mode:agent", stored.PaxConfig.PermissionChoiceID)
}

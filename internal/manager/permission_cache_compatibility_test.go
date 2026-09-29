package manager

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestConfigurationIdentityGivenPaxdUpgradeWhenParsedThenKeepsPermissionCache(t *testing.T) {
	report := nodeControlACPPoolCapabilityReport{SchemaVersion: 2, ProtocolVersion: 1,
		ClientProfileHash: "paxd-0.1.40", ClientCapabilitiesHash: "boolean-supported",
		CommandFingerprint: "command", WorkerResultHash: "worker", PoolConsistency: "consistent"}
	report.Implementation.IdentityFingerprint = "claude-0.49.0"
	parse := func(r nodeControlACPPoolCapabilityReport) *domain.AgentRuntimeIdentity {
		raw, err := json.Marshal(r)
		require.NoError(t, err)
		identity, err := parseAgentRuntimeIdentity(
			"claude-air",
			"claude-code",
			"conn",
			raw,
			time.Now(),
		)
		require.NoError(t, err)
		return identity
	}
	before := parse(report)
	report.ClientProfileHash = "paxd-0.1.41"
	after := parse(report)
	require.NotEmpty(t, before.ConfigurationFingerprint)
	assert.Equal(t, before.ConfigurationFingerprint, after.ConfigurationFingerprint)
	assert.NotEqual(t, before.IdentityFingerprint, after.IdentityFingerprint)
	for _, field := range []string{"capabilities", "command", "worker", "implementation", "protocol"} {
		t.Run(field, func(t *testing.T) {
			changed := report
			switch field {
			case "capabilities":
				changed.ClientCapabilitiesHash = "changed"
			case "command":
				changed.CommandFingerprint = "changed"
			case "worker":
				changed.WorkerResultHash = "changed"
			case "implementation":
				changed.Implementation.IdentityFingerprint = "changed"
			case "protocol":
				changed.ProtocolVersion++
			}
			assert.NotEqual(
				t,
				after.ConfigurationFingerprint,
				parse(changed).ConfigurationFingerprint,
			)
		})
	}
	report.ClientCapabilitiesHash = ""
	assert.Empty(t, parse(report).ConfigurationFingerprint)
}

func TestPermissionCacheGivenLegacyClaudeObservationWhenIdentityChangesThenShowsUnverifiedChoices(
	t *testing.T,
) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(t.Context(), owner, domain.RegisterAgentRequest{
		Name: "claude-air", AgentType: "claude-code", Hostname: "host", OS: "darwin",
	}, "key")
	require.NoError(t, err)
	srv := &Service{store: newCanonicalSessionStore(store), clock: func() time.Time { return now }}
	identity := domain.AgentRuntimeIdentity{
		AgentID:             agent.AgentID,
		IdentityFingerprint: "sha256:eed02329436f9eb6cb4d57bdcfc06c9efdd1f12092134dd564bdca9924cd3395",
		PoolConsistency:     "consistent",
		ReportGeneration:    1,
	}
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
	live := json.RawMessage(
		`{"configOptions":[{"id":"mode","category":"mode","currentValue":"default","options":[{"value":"auto","name":"Auto"},{"value":"default","name":"Default"},{"value":"acceptEdits","name":"Accept Edits"},{"value":"plan","name":"Plan Mode"},{"value":"dontAsk","name":"Don't Ask"},{"value":"bypassPermissions","name":"Bypass Permissions"}]}]}`,
	)
	require.NoError(t, srv.observePermissionCatalog(t.Context(), agent.AgentID, live))
	identity.IdentityFingerprint = "new"
	identity.ConfigurationFingerprint = "config-v1:new"
	identity.ReportGeneration++
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
	catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
	require.NoError(t, err)
	require.Len(t, catalog.Choices, 7)
	assert.True(t, catalog.Stale)
	assert.Empty(t, catalog.DefaultChoiceID)
	assert.True(t, catalog.Choices[1].RequiresConfirmation)
	choice := catalog.Choices[4].ChoiceID
	_, err = srv.resolveStoredPermissionChoice(t.Context(), agent, choice)
	assert.Error(t, err, "display fallback must not authorize existing session changes")
	_, err = resolvePermissionChoiceFromLive(
		choice,
		parseObservedPermissionCatalog(
			json.RawMessage(`{"modes":{"availableModes":[{"id":"default"}]}}`),
		),
		domain.AgentPermissionProfile{},
		false,
	)
	assert.Error(t, err, "stale selections must be checked against the new session")
	require.NoError(t, srv.observePermissionCatalog(t.Context(), agent.AgentID, live))
	_, err = store.GetPermissionObservation(t.Context(), agent.AgentID, "config-v1:new")
	require.NoError(t, err)
	_, err = srv.resolveStoredPermissionChoice(t.Context(), agent, choice)
	require.NoError(t, err)
	identity.IdentityFingerprint = "newer-paxd"
	identity.ReportGeneration++
	require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
	catalog, err = srv.resolveAgentPermissionCatalog(t.Context(), agent)
	require.NoError(t, err)
	assert.False(t, catalog.Stale)
	require.Len(t, catalog.Choices, 7)
}

func TestPermissionCacheGivenLegacyFallbackWhenResolvedThenHonorsInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pool          string
		fingerprint   string
		expired       bool
		negative      bool
		exactNegative bool
		wantNative    bool
	}{
		{name: "legacy current", pool: "consistent", fingerprint: "old", wantNative: true},
		{name: "expired legacy", pool: "consistent", fingerprint: "old", expired: true},
		{name: "mixed pool", pool: "mixed", fingerprint: "old"},
		{name: "unknown pool", pool: "unknown", fingerprint: "old"},
		{name: "known incompatible configuration", pool: "consistent", fingerprint: "config-v1:other"},
		{name: "negative legacy", pool: "consistent", fingerprint: "old", negative: true},
		{name: "exact negative wins", pool: "consistent", fingerprint: "old", exactNegative: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			identity := domain.AgentRuntimeIdentity{
				AgentID:                  agent.AgentID,
				IdentityFingerprint:      "current",
				ConfigurationFingerprint: "config-v1:current",
				PoolConsistency:          tc.pool,
			}
			require.NoError(t, store.UpsertAgentRuntimeIdentity(t.Context(), identity))
			observed := parseObservedPermissionCatalog(
				json.RawMessage(`{"modes":{"availableModes":[{"id":"plan","name":"Plan"}]}}`),
			)
			if tc.negative {
				observed = domain.ObservedPermissionCatalog{}
			}
			expires := now.Add(time.Hour)
			if tc.expired {
				expires = now.Add(-time.Second)
			}
			_, err = store.UpsertPermissionObservation(
				t.Context(),
				domain.AgentPermissionObservation{
					AgentID: agent.AgentID, IdentityFingerprint: tc.fingerprint, Catalog: observed, ObservedAt: now, ExpiresAt: expires,
				},
			)
			require.NoError(t, err)
			if tc.exactNegative {
				_, err = store.UpsertPermissionObservation(
					t.Context(),
					domain.AgentPermissionObservation{
						AgentID: agent.AgentID, IdentityFingerprint: identity.ConfigurationFingerprint, ObservedAt: now, ExpiresAt: expires,
					},
				)
				require.NoError(t, err)
			}
			srv := &Service{
				store: newCanonicalSessionStore(store),
				clock: func() time.Time { return now },
			}
			catalog, err := srv.resolveAgentPermissionCatalog(t.Context(), agent)
			require.NoError(t, err)
			if tc.wantNative {
				assert.Len(t, catalog.Choices, 2)
			} else {
				assert.Len(t, catalog.Choices, 1)
			}
		})
	}
}

func TestPermissionCacheGivenProfileMappedLegacyChoicesWhenDisplayedThenPreservesLiveChoiceIDs(
	t *testing.T,
) {
	now := time.Now().UTC()
	store := NewMemoryStore(func() time.Time { return now })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	agent, err := store.RegisterAgent(
		t.Context(),
		owner,
		domain.RegisterAgentRequest{Name: "Codex", AgentType: "codex"},
		"key",
	)
	require.NoError(t, err)
	profile := domain.BuiltInCodexPermissionProfile(now)
	live := parseObservedPermissionCatalog(
		json.RawMessage(
			`{"configOptions":[{"id":"mode","category":"mode","currentValue":"agent","options":[{"value":"agent","name":"Agent"}]}]}`,
		),
	)
	_, err = store.UpsertPermissionObservation(
		t.Context(),
		domain.AgentPermissionObservation{
			AgentID:             agent.AgentID,
			IdentityFingerprint: "legacy",
			Catalog:             live,
			ObservedAt:          now,
			ExpiresAt:           now.Add(time.Hour),
		},
	)
	require.NoError(t, err)
	srv := &Service{store: newCanonicalSessionStore(store), clock: func() time.Time { return now }}
	catalog, found, err := srv.legacyPermissionCatalogForDisplay(
		t.Context(),
		agent.AgentID,
		profile,
		true,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, catalog.DefaultChoiceID)
	require.Len(t, catalog.Choices, 2)
	choice := catalog.Choices[1]
	assert.Equal(t, "agent:mode:agent", choice.ChoiceID)
	assert.Equal(t, "unknown", choice.Risk)
	assert.True(t, choice.RequiresConfirmation)
	_, err = resolvePermissionChoiceFromLive(choice.ChoiceID, live, profile, true)
	require.NoError(t, err)
}

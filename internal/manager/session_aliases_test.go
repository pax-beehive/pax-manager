package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type sessionAliasLookupProbe struct {
	domain.Store
	aliases domain.SessionAliases
	err     error
	calls   [][3]string
}

func (s *sessionAliasLookupProbe) ResolveSessionAliases(
	_ context.Context,
	owner, agent, id string,
) (domain.SessionAliases, error) {
	s.calls = append(s.calls, [3]string{owner, agent, id})
	return s.aliases, s.err
}

func TestSessionAliasesGivenProductionWrapperThenForwardInternalLookupAndCacheBothIDs(
	t *testing.T,
) {
	probe := &sessionAliasLookupProbe{
		aliases: domain.SessionAliases{SessionID: "sess_secretary_run", NativeID: "native"},
	}
	wrapped := newCanonicalSessionStore(probe)
	agent := &ACPTunnelAgent{agentID: "agent", ownerUserID: "owner"}
	for _, id := range []string{"native", "sess_secretary_run", "native"} {
		aliases, ok := agent.resolveSessionAliases(t.Context(), wrapped, id)
		require.True(t, ok)
		assert.Equal(t, "sess_secretary_run", aliases.managerID)
		assert.Equal(t, "native", aliases.nativeID)
	}
	assert.Equal(t, [][3]string{{"owner", "agent", "native"}}, probe.calls)
	canonical, err := wrapped.(canonicalSessionStore).canonicalSessionID(
		t.Context(),
		"owner",
		"agent",
		"native",
	)
	require.NoError(t, err)
	assert.Equal(t, "sess_secretary_run", canonical)
}

func TestSessionAliasesGivenStorageFailureThenDoNotCacheOrFallBackToUserList(t *testing.T) {
	for _, failure := range []error{domain.ErrNotFound, context.Canceled, domain.ErrConflict} {
		probe := &sessionAliasLookupProbe{err: failure}
		wrapped := newCanonicalSessionStore(probe)
		agent := &ACPTunnelAgent{agentID: "agent", ownerUserID: "owner"}
		for range 2 {
			_, ok := agent.resolveSessionAliases(t.Context(), wrapped, "native")
			assert.False(t, ok)
		}
		assert.Len(t, probe.calls, 2)
		assert.Equal(
			t,
			"native",
			canonicalACPHistorySessionID(t.Context(), wrapped, "owner", "agent", "native"),
		)
		canonical, err := wrapped.(canonicalSessionStore).canonicalSessionID(
			t.Context(),
			"owner",
			"agent",
			"native",
		)
		require.NoError(t, err)
		assert.Equal(t, "native", canonical)
	}
}

type failingAliasListStore struct{ domain.Store }

func (failingAliasListStore) ListAgentSessions(
	context.Context,
	domain.UserPrincipal,
	string,
) ([]domain.AgentSession, error) {
	return nil, context.Canceled
}

func TestSessionAliasesGivenCustomStoreListFailureThenPreserveFailure(t *testing.T) {
	_, err := resolveStoreSessionAliases(
		t.Context(),
		failingAliasListStore{},
		"owner",
		"agent",
		"native",
	)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestTunnelSessionAliasesGivenInternalLookupThenPreserveRoutingAndFailureSemantics(
	t *testing.T,
) {
	probe := &sessionAliasLookupProbe{
		aliases: domain.SessionAliases{SessionID: "manager", NativeID: "native"},
	}
	server := &Service{store: newCanonicalSessionStore(probe)}
	agent := &ACPTunnelAgent{store: server.store, agentID: "agent", ownerUserID: "owner"}
	assert.Empty(t, server.virtualACPSessionID(t.Context(), "owner", "agent", ""))
	assert.Empty(t, server.nativeACPSessionID(t.Context(), "owner", "agent", ""))
	assert.Equal(t, "manager", server.virtualACPSessionID(t.Context(), "owner", "agent", "native"))
	assert.Equal(t, "native", server.nativeACPSessionID(t.Context(), "owner", "agent", "manager"))
	native, err := agent.nativeSessionID(t.Context(), "manager")
	require.NoError(t, err)
	assert.Equal(t, "native", native)
	_, err = agent.nativeSessionID(t.Context(), "native")
	assert.ErrorContains(t, err, "not found")
	probe.aliases.NativeID = ""
	assert.Equal(t, "manager", server.nativeACPSessionID(t.Context(), "owner", "agent", "manager"))
	_, err = agent.nativeSessionID(t.Context(), "manager")
	assert.ErrorContains(t, err, "no native ACP session id")
	probe.err = context.Canceled
	assert.Equal(t, "raw", server.virtualACPSessionID(t.Context(), "owner", "agent", "raw"))
	assert.Equal(t, "raw", server.nativeACPSessionID(t.Context(), "owner", "agent", "raw"))
	_, err = agent.nativeSessionID(t.Context(), "manager")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = agent.nativeSessionID(t.Context(), " ")
	assert.ErrorContains(t, err, "manager session id is required")
	_, err = (&ACPTunnelAgent{}).nativeSessionID(t.Context(), "manager")
	assert.ErrorContains(t, err, "session store is required")
}

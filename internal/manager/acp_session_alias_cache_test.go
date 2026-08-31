package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

// countingListSessionsStore counts ListAgentSessions calls to prove the alias
// cache removes per-frame session reads.
type countingListSessionsStore struct {
	domain.Store
	mu    sync.Mutex
	calls int
}

func (c *countingListSessionsStore) ListAgentSessions(
	ctx context.Context,
	principal domain.UserPrincipal,
	agentID string,
) ([]domain.AgentSession, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.Store.ListAgentSessions(ctx, principal, agentID)
}

func (c *countingListSessionsStore) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newAliasTestAgent(t *testing.T) (*ACPTunnelAgent, *countingListSessionsStore, domain.AgentSession) {
	t.Helper()
	ctx := context.Background()
	base := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	})
	user, err := base.EnsureUser(ctx, "t@example.com", "T", "user")
	require.NoError(t, err)
	agentModel, err := base.RegisterAgent(ctx, user, domain.RegisterAgentRequest{Name: "a", OS: "darwin"}, "hash")
	require.NoError(t, err)
	session, err := base.CreateNodeAgentSession(ctx, domain.UserPrincipal{User: user}, domain.CreateSessionRequest{
		NodeID:    agentModel.NodeID,
		AgentID:   agentModel.AgentID,
		SessionID: "sess_manager",
		NativeID:  "native-1",
	})
	require.NoError(t, err)

	store := &countingListSessionsStore{Store: base}
	agent := &ACPTunnelAgent{
		agentID:     agentModel.AgentID,
		nodeID:      agentModel.NodeID,
		ownerUserID: user.UserID,
		sessionID:   session.NativeID,
	}
	return agent, store, session
}

func TestResolveSessionAliasesCachesAfterFirstLookup(t *testing.T) {
	ctx := context.Background()
	agent, store, session := newAliasTestAgent(t)

	// First lookup by native ID reads the store once and caches both aliases.
	aliases, ok := agent.resolveSessionAliases(ctx, store, "native-1")
	require.True(t, ok)
	assert.Equal(t, session.SessionID, aliases.managerID)
	assert.Equal(t, "native-1", aliases.nativeID)
	assert.Equal(t, 1, store.count())

	// Subsequent lookups by either alias are served from cache — no more reads.
	for i := 0; i < 5; i++ {
		_, ok := agent.resolveSessionAliases(ctx, store, "native-1")
		require.True(t, ok)
		_, ok = agent.resolveSessionAliases(ctx, store, session.SessionID)
		require.True(t, ok)
	}
	assert.Equal(t, 1, store.count(), "cached aliases must not re-query the store")

	assert.Equal(t, session.SessionID, agent.canonicalSessionID(ctx, store, "native-1"))
	assert.Equal(t, 1, store.count())
}

func TestResolveSessionAliasesDoesNotCacheMisses(t *testing.T) {
	ctx := context.Background()
	agent, store, _ := newAliasTestAgent(t)

	// Unknown IDs must be re-resolved every time (a frame may precede its
	// durable session row); misses are never pinned.
	_, ok := agent.resolveSessionAliases(ctx, store, "not-a-session")
	assert.False(t, ok)
	_, ok = agent.resolveSessionAliases(ctx, store, "not-a-session")
	assert.False(t, ok)
	assert.Equal(t, 2, store.count())
}

func TestPrimeSessionAliasesAvoidsAnyStoreRead(t *testing.T) {
	ctx := context.Background()
	agent, store, _ := newAliasTestAgent(t)

	agent.primeSessionAliases("sess_manager", "native-1")

	aliases, ok := agent.resolveSessionAliases(ctx, store, "native-1")
	require.True(t, ok)
	assert.Equal(t, "sess_manager", aliases.managerID)
	assert.Equal(t, "sess_manager", agent.canonicalSessionID(ctx, store, "native-1"))
	assert.Equal(t, 0, store.count(), "primed aliases must serve without any store read")
}

func TestCanonicalACPHistorySessionIDCachedUsesAgentCache(t *testing.T) {
	ctx := context.Background()
	agent, store, session := newAliasTestAgent(t)
	agent.store = store
	agent.primeSessionAliases(session.SessionID, "native-1")

	sink := acpAgentHistoryTextSink{agent: agent}
	got := canonicalACPHistorySessionIDCached(ctx, store, sink, agent.ownerUserID, agent.agentID, "native-1")
	assert.Equal(t, session.SessionID, got)
	assert.Equal(t, 0, store.count(), "agent-backed sink resolves via cache")

	// A non-agent sink falls back to the direct store lookup.
	got = canonicalACPHistorySessionIDCached(ctx, store, immediateACPHistoryTextSink{store: store},
		agent.ownerUserID, agent.agentID, "native-1")
	assert.Equal(t, session.SessionID, got)
	assert.Equal(t, 1, store.count(), "fallback path performs the store read")
}

func TestSessionAliasCacheGuards(t *testing.T) {
	ctx := context.Background()
	agent, store, _ := newAliasTestAgent(t)

	// Unknown ID resolves to the raw session ID without caching anything.
	assert.Equal(t, "unknown", agent.canonicalSessionID(ctx, store, "unknown"))

	// Empty / nil inputs are safe no-ops.
	_, ok := agent.resolveSessionAliases(ctx, store, "")
	assert.False(t, ok)
	var nilAgent *ACPTunnelAgent
	_, ok = nilAgent.resolveSessionAliases(ctx, store, "x")
	assert.False(t, ok)
	nilAgent.primeSessionAliases("m", "n")        // must not panic
	agent.primeSessionAliases("", "native-1")     // ignored: incomplete pair
	agent.primeSessionAliases("sess_manager", "") // ignored: incomplete pair

	_, ok = agent.resolveSessionAliases(ctx, store, "native-1")
	assert.True(t, ok, "incomplete primes must not shadow the real mapping")
}

func TestResolveSessionAliasesNilStore(t *testing.T) {
	agent, _, _ := newAliasTestAgent(t)
	_, ok := agent.resolveSessionAliases(context.Background(), nil, "native-1")
	assert.False(t, ok, "no store means the mapping cannot be resolved yet")
}

func TestACPRuntimeProjectorSkipsPureTerminalDelta(t *testing.T) {
	ctx := context.Background()
	recorder := &runtimeStateRecorder{}
	projector := newACPRuntimeProjector(recorder, func() time.Time {
		return time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	})
	agent := &ACPTunnelAgent{agentID: "a", nodeID: "n", ownerUserID: "u", sessionID: "s"}

	observe := func(raw []byte) {
		t.Helper()
		frame := newACPFrameContext(agent, acpAgentToUser, websocket.TextMessage, raw)
		frame.managerSessionID = "s"
		require.NoError(t, projector.Observe(ctx, frame))
	}

	// A pure terminal-output delta (no status) must not churn runtime state.
	observe(terminalDeltaFrame("s", "call_1", " M first.go\n"))
	assert.Empty(t, recorder.states, "terminal output is not a tool status transition")

	// A real status transition on the same tool still writes runtime state.
	observe([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"call_1","status":"completed"}}}`))
	require.Len(t, recorder.states, 1)
	assert.Equal(t, "call_1", recorder.last().ActiveToolCalls[0].ToolCallID)
}

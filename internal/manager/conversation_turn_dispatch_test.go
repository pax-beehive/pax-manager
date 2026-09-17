package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func reportQueueTestSnapshot(
	t *testing.T,
	srv *Server,
	fixture conversationTestFixture,
	sequence int64,
	turns []nodeControlSessionRuntimeActiveTurn,
) {
	t.Helper()
	node, err := srv.store.GetNode(
		t.Context(),
		testUserPrincipal(t, srv, fixture.userEmail),
		fixture.nodeID,
	)
	require.NoError(t, err)
	require.NoError(
		t,
		srv.store.(domain.SessionRuntimeSnapshotStore).ActivateNodeRuntimeFence(
			t.Context(),
			node,
			"queue-fence",
		),
	)
	require.NoError(
		t,
		srv.replaceSessionRuntimeSnapshot(t.Context(), node, "queue-fence", nodeControlReport{
			SessionRuntimeSnapshot: &nodeControlSessionRuntimeSnapshot{
				AgentID: fixture.agentID, SchemaVersion: 1, Sequence: sequence, GeneratedAt: time.Now().UTC(), ActiveTurns: turns,
			},
		}),
	)
}

func TestQueuedTurnRunsWithoutBrowserOnRepeatedIdleSnapshot(t *testing.T) {
	t.Run("already idle", func(t *testing.T) { testQueuedTurnFromSnapshot(t, false) })
	t.Run("approval finishes", func(t *testing.T) { testQueuedTurnFromSnapshot(t, true) })
}
func testQueuedTurnFromSnapshot(t *testing.T, approval bool) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	createConversationTestSession(t, srv, fixture, "queued-session", "queued-native")
	var initial []nodeControlSessionRuntimeActiveTurn
	if approval {
		initial = []nodeControlSessionRuntimeActiveTurn{
			{
				NativeSessionID:   "queued-native",
				TurnID:            "old-turn",
				PromptRequestID:   json.RawMessage("41"),
				RuntimeStatus:     "waiting_approval",
				PendingApprovalID: json.RawMessage(`"approval"`),
			},
		}
	}
	reportQueueTestSnapshot(t, srv, fixture, 1, initial)
	require.Eventually(
		t,
		func() bool { _, busy := srv.queueChecks.Load(fixture.agentID); return !busy },
		time.Second,
		time.Millisecond,
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	server := httptest.NewServer(mux)
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(
			server.URL,
			"http",
		)+"/api/v1/agent/tunnel?agent_id="+fixture.agentID,
		http.Header{"X-Pax-Key": []string{fixture.nodeAPIKey}},
	)
	require.NoError(t, err)
	defer func() { _ = ws.Close() }()
	completeMockAgentReconcile(t, ws, fixture.agentID, 1)
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	turn, _, err := srv.conversationTurns.upsert(
		t.Context(),
		conversationQueuedTurn{
			AgentID:   fixture.agentID,
			SessionID: "queued-session",
			OwnerID:   principal.User.UserID,
			CommandID: "queue-command",
			Input:     "detached work",
		},
	)
	require.NoError(t, err)
	// There is no conversation HTTP request, and idle did not change.
	if approval {
		reportQueueTestSnapshot(t, srv, fixture, 2, initial)
		require.Eventually(
			t,
			func() bool { _, busy := srv.queueChecks.Load(fixture.agentID); return !busy },
			time.Second,
			time.Millisecond,
		)
		pending, ok, err := srv.conversationTurns.get(
			t.Context(),
			fixture.agentID,
			"queued-session",
		)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "queued", pending.State)
	}
	reportQueueTestSnapshot(t, srv, fixture, 3, nil)
	prompt := readNextManagerToAgentData(t, ws)
	require.Equal(t, turn.TurnID, prompt.Metadata["turn_id"])
	require.Contains(t, string(prompt.Payload), "detached work")
	promptID := acpPayloadRequestID(t, prompt.Payload)
	// Acceptance retires the small queue row before execution finishes.
	reportQueueTestSnapshot(t, srv, fixture, 4, []nodeControlSessionRuntimeActiveTurn{{
		NativeSessionID: "queued-native", TurnID: turn.TurnID, PromptRequestID: json.RawMessage(promptID), RuntimeStatus: "running",
	}})
	require.Eventually(t, func() bool {
		_, ok, err := srv.conversationTurns.get(t.Context(), fixture.agentID, "queued-session")
		return err == nil && !ok
	}, time.Second, time.Millisecond)
	writeAgentDataFrame(
		t,
		ws,
		prompt.QueueID,
		1,
		json.RawMessage(`{"jsonrpc":"2.0","id":`+promptID+`,"result":{"stopReason":"end_turn"}}`),
	)
	require.Eventually(t, func() bool {
		messages, err := srv.store.ListMessages(t.Context(), fixture.agentID, "queued-session", 100)
		for _, msg := range messages {
			if msg.TurnID == turn.TurnID && msg.MessageType == "turn_done" {
				return err == nil
			}
		}
		return false
	}, time.Second, time.Millisecond)
}

// A slow database must not hold up the high-frequency snapshot reader.
type blockingQueueStore struct {
	domain.TurnQueueStore
	entered chan struct{}
	unblock chan struct{}
}

func (s *blockingQueueStore) ListQueuedTurns(
	ctx context.Context,
	nodeID, agentID string,
) ([]domain.QueuedTurn, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.unblock:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestQueueSnapshotWakeIsNonBlockingAndCoalesced(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	blocking := &blockingQueueStore{
		TurnQueueStore: srv.conversationTurns.store,
		entered:        make(chan struct{}, 1),
		unblock:        make(chan struct{}),
	}
	srv.conversationTurns.store = blocking
	defer close(blocking.unblock)
	snapshot := domain.AgentRuntimeSnapshot{AgentID: "agent"}
	srv.wakeQueuedTurns(t.Context(), Node{NodeID: "node"}, snapshot)
	select {
	case <-blocking.entered:
	case <-time.After(time.Second):
		t.Fatal("queue check did not start")
	}
	for i := 0; i < 1000; i++ {
		srv.wakeQueuedTurns(t.Context(), Node{NodeID: "node"}, snapshot)
	}
	require.Len(t, srv.queueCheckSlots, 1)
}

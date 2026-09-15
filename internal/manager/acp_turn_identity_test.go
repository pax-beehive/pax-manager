package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"

	"github.com/gorilla/websocket"
	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionEnvelopePersistsTurnIDForReplay(t *testing.T) {
	store := NewMemoryStore(time.Now)
	producer, err := reliablemq.NewProducer(
		t.Context(),
		reliablemq.ProducerConfig{QueueID: "queue_turn", Stream: reliablemq.StreamACP},
		store,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, producer.Close(context.Background())) })
	agent := &ACPTunnelAgent{
		agentID:        "agent_1",
		connectionID:   "queue_turn",
		store:          store,
		transportStore: store,
	}
	agent.reliableEngine = reliablemq.NewEngine(reliablemq.Config{}, store, producer, nil)
	payload := []byte(`{"jsonrpc":"2.0","id":10,"method":"session/prompt"}`)
	require.NoError(
		t,
		agent.writeSessionFrame(
			t.Context(),
			"sess_1",
			"native_1",
			websocket.TextMessage,
			payload,
			"turn_first",
		),
	)
	require.NoError(
		t,
		agent.writeWorkerResponse(
			t.Context(),
			"sess_1",
			"native_1",
			websocket.TextMessage,
			[]byte(
				`{"id":"permission_1","result":{"outcome":{"outcome":"cancelled"}}}`,
			),
			"turn_first",
		),
	)
	_, err = producer.Checkpoint(t.Context())
	require.NoError(t, err)
	frames, err := store.ListOutboundReplay(t.Context(), "queue_turn", reliablemq.StreamACP, 10)
	require.NoError(t, err)
	require.Len(t, frames, 2)
	assert.Equal(t, "turn_first", frames[1].Metadata["turn_id"])
	assert.Equal(t, "turn_first", frames[0].Metadata["turn_id"])
	assert.Equal(t, "sess_1", frames[0].Metadata["manager_session_id"])
	assert.Equal(t, payload, []byte(frames[0].Payload))
}

func TestLateTaggedOutputDoesNotChangeCurrentTurn(t *testing.T) {
	projector := newACPRuntimeProjector(time.Now)
	agent := &ACPTunnelAgent{agentID: "agent_1"}
	observe := func(direction acpFrameDirection, turnID, payload string) {
		frame := newACPFrameContext(agent, direction, websocket.TextMessage, []byte(payload))
		frame.managerSessionID = "sess_1"
		frame.businessTurnID = turnID
		if direction == acpAgentToUser {
			frame.transportMetadata = map[string]string{"turn_id": turnID}
		}
		require.NoError(t, projector.Observe(t.Context(), frame))
	}
	observe(acpUserToAgent, "turn_first", `{"id":10,"method":"session/prompt"}`)
	observe(acpUserToAgent, "turn_second", `{"id":11,"method":"session/prompt"}`)
	observe(acpAgentToUser, "turn_first", `{"id":10,"result":{"stopReason":"end_turn"}}`)
	assert.Equal(t, "turn_second", testACPRuntimeState(projector, "agent_1", "sess_1").ActiveTurnID)
	assert.Equal(t, "running", testACPRuntimeState(projector, "agent_1", "sess_1").Lifecycle)
	assert.Len(t, projector.sessions, 1)
}

func TestRuntimeSnapshotRejectsConflictingTurnAliases(t *testing.T) {
	err := new(
		Server,
	).replaceSessionRuntimeSnapshot(t.Context(), Node{}, "fence", nodeControlReport{
		SessionRuntimeSnapshot: &nodeControlSessionRuntimeSnapshot{
			SchemaVersion: 1,
			ActiveTurns: []nodeControlSessionRuntimeActiveTurn{
				{TurnID: "turn_new", TurnInstanceID: "turn_old"},
			},
		},
	})
	require.EqualError(t, err, "snapshot turn id aliases disagree")
}

func TestRawPromptsReceiveDistinctManagerTurnIDs(t *testing.T) {
	middleware := acpRuntimeStateMiddleware{
		projector: newACPRuntimeProjector(time.Now),
	}
	agent := &ACPTunnelAgent{agentID: "agent_1"}
	var ids []string
	for i := 0; i < 2; i++ {
		frame := newACPFrameContext(
			agent,
			acpUserToAgent,
			websocket.TextMessage,
			[]byte(`{"id":10,"method":"session/prompt"}`),
		)
		frame.managerSessionID = "sess_1"
		require.NoError(
			t,
			middleware.HandleACPFrame(
				t.Context(),
				frame,
				func(_ context.Context, f *acpFrameContext) error { ids = append(ids, f.businessTurnID); return nil },
			),
		)
	}
	require.NotEmpty(t, ids[0])
	assert.NotEqual(t, ids[0], ids[1])
}

func TestReusedToolIDDoesNotOverwritePreviousTurn(t *testing.T) {
	store := NewMemoryStore(time.Now)
	first := json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"sess_1","update":{"sessionUpdate":"tool_call","toolCallId":"tool_reused","title":"first execution","status":"in_progress"}}}`,
	)
	second := json.RawMessage(
		`{"method":"session/update","params":{"sessionId":"sess_1","update":{"sessionUpdate":"tool_call","toolCallId":"tool_reused","title":"second execution","status":"completed"}}}`,
	)
	for i, raw := range []json.RawMessage{first, second} {
		turnID := []string{"turn_first", "turn_second"}[i]
		require.NoError(
			t,
			projectACPTransportMessageWithTextSinkForTurn(
				t.Context(),
				store,
				nil,
				"agent_1",
				"user_1",
				"node_1",
				domain.TransportStreamPaxdToManager,
				int64(i+1),
				"",
				"sess_1",
				turnID,
				raw,
			),
		)
	}
	messages, err := store.ListMessages(t.Context(), "agent_1", "sess_1", 10)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	byTurn := map[string]domain.Message{}
	for _, message := range messages {
		byTurn[message.TurnID] = message
	}
	assert.Contains(t, string(byTurn["turn_first"].RawJSON), "first execution")
	assert.NotContains(t, string(byTurn["turn_first"].RawJSON), "second execution")
	assert.NotEqual(t, byTurn["turn_first"].MessageID, byTurn["turn_second"].MessageID)
}

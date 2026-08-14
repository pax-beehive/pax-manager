package manager

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type runtimeStateRecorder struct {
	mu     sync.Mutex
	states []domain.SessionRuntimeState
}

func (r *runtimeStateRecorder) UpdateSessionRuntimeState(
	ctx context.Context,
	state domain.SessionRuntimeState,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
	return nil
}

func (r *runtimeStateRecorder) last() domain.SessionRuntimeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return domain.SessionRuntimeState{}
	}
	return r.states[len(r.states)-1]
}

func TestACPRuntimeProjectorTracksPromptPermissionAndCompletion(t *testing.T) {
	recorder := &runtimeStateRecorder{}
	now := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	projector := newACPRuntimeProjector(recorder, func() time.Time { return now })
	agent := &ACPTunnelAgent{
		agentID:     "agent_1",
		nodeID:      "node_1",
		ownerUserID: "usr_1",
		sessionID:   "sess_1",
	}

	observe := func(direction acpFrameDirection, raw string) {
		t.Helper()
		frame := newACPFrameContext(agent, direction, websocket.TextMessage, []byte(raw))
		frame.managerSessionID = "sess_1"
		if err := projector.Observe(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
	}

	observe(acpUserToAgent, `{
		"jsonrpc":"2.0",
		"id":1,
		"method":"session/prompt",
		"params":{"sessionId":"sess_1","turn_id":"turn_business","prompt":[{"type":"text","text":"hi"}]}
	}`)
	state := recorder.last()
	if state.Lifecycle != domain.RuntimeLifecycleRunning ||
		state.ActivePromptRequestID != "1" ||
		state.ActiveTurnID != "turn_business" {
		t.Fatalf("after prompt start state = %+v", state)
	}

	observe(acpAgentToUser, `{
		"jsonrpc":"2.0",
		"method":"session/update",
		"params":{
			"sessionId":"sess_1",
			"update":{
				"sessionUpdate":"tool_call",
				"toolCallId":"call_1",
				"kind":"execute",
				"title":"go test ./...",
				"status":"pending"
			}
		}
	}`)
	state = recorder.last()
	if len(state.ActiveToolCalls) != 1 ||
		state.ActiveToolCalls[0].ToolCallID != "call_1" ||
		state.ActiveToolCalls[0].Status != "pending" {
		t.Fatalf("after tool pending state = %+v", state)
	}

	observe(acpAgentToUser, `{
		"jsonrpc":"2.0",
		"id":"perm_1",
		"method":"session/request_permission",
		"params":{
			"sessionId":"sess_1",
			"toolCall":{
				"toolCallId":"call_1",
				"kind":"execute",
				"title":"go test ./...",
				"status":"pending"
			},
			"options":[{"optionId":"allow","kind":"allow_once"}]
		}
	}`)
	state = recorder.last()
	if state.Lifecycle != domain.RuntimeLifecycleWaitingApproval ||
		state.BlockedReason != domain.RuntimeBlockedReasonToolApproval ||
		state.PendingApprovalID != "perm_1" {
		t.Fatalf("after permission request state = %+v", state)
	}

	observe(acpUserToAgent, `{
		"jsonrpc":"2.0",
		"id":"perm_1",
		"result":{"optionId":"allow","kind":"allow_once"}
	}`)
	state = recorder.last()
	if state.Lifecycle != domain.RuntimeLifecycleRunning ||
		state.BlockedReason != "" ||
		state.PendingApprovalID != "" {
		t.Fatalf("after permission resolved state = %+v", state)
	}

	observe(acpAgentToUser, `{
		"jsonrpc":"2.0",
		"id":1,
		"result":{"stopReason":"end_turn"}
	}`)
	state = recorder.last()
	if state.Lifecycle != domain.RuntimeLifecycleIdle ||
		state.LastStopReason != "end_turn" ||
		state.ActivePromptRequestID != "" ||
		state.ActiveTurnID != "" {
		t.Fatalf("after prompt completion state = %+v", state)
	}
}

func TestACPRuntimeProjectorIsConcurrentSafe(t *testing.T) {
	recorder := &runtimeStateRecorder{}
	projector := newACPRuntimeProjector(recorder, time.Now)
	agent := &ACPTunnelAgent{
		agentID:     "agent_1",
		nodeID:      "node_1",
		ownerUserID: "usr_1",
		sessionID:   "sess_1",
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      i,
				"method":  "session/prompt",
				"params": map[string]any{
					"sessionId": "sess_1",
					"prompt":    []map[string]string{{"type": "text", "text": "hi"}},
				},
			})
			frame := newACPFrameContext(agent, acpUserToAgent, websocket.TextMessage, raw)
			frame.managerSessionID = "sess_1"
			if err := projector.Observe(context.Background(), frame); err != nil {
				t.Errorf("observe: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if state := recorder.last(); state.AgentID != "agent_1" ||
		state.SessionID != "sess_1" ||
		state.Lifecycle != domain.RuntimeLifecycleRunning {
		t.Fatalf("last state = %+v", state)
	}
}

func TestACPApprovalMiddlewareGivenSessionAutoApproveAllThenAllowsAutoApprove(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	require.NoError(t, err)
	agentModel, err := store.RegisterAgent(ctx, user, domain.RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	require.NoError(t, err)
	session, err := store.CreateNodeAgentSession(
		ctx,
		domain.UserPrincipal{User: user},
		domain.CreateSessionRequest{
			NodeID:    agentModel.NodeID,
			AgentID:   agentModel.AgentID,
			SessionID: "sess_manager_1",
			NativeID:  "harness-session-1",
			PaxConfig: domain.SessionPaxConfig{
				ApprovalMode: domain.SessionApprovalModeAutoApproveAll,
			},
		},
	)
	require.NoError(t, err)
	require.NoError(t, store.UpdateSessionRuntimeState(ctx, domain.SessionRuntimeState{
		OwnerUserID:           user.UserID,
		NodeID:                agentModel.NodeID,
		AgentID:               agentModel.AgentID,
		SessionID:             session.SessionID,
		Lifecycle:             domain.RuntimeLifecycleRunning,
		ActivePromptRequestID: "prompt_1",
		ActiveTurnID:          "turn_business",
		UpdatedAt:             time.Now().UTC(),
	}))
	agent := &ACPTunnelAgent{
		agentID:     agentModel.AgentID,
		nodeID:      agentModel.NodeID,
		ownerUserID: user.UserID,
		sessionID:   session.NativeID,
	}
	frame := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		[]byte(`{
			"jsonrpc":"2.0",
			"id":"perm_1",
			"method":"session/request_permission",
			"params":{
				"sessionId":"harness-session-1",
				"toolCall":{"toolCallId":"call_1","kind":"execute","title":"go test ./..."},
				"options":[{"optionId":"allow","kind":"allow_once"}]
			}
		}`),
	)
	frame.managerSessionID = session.SessionID

	middleware := acpApprovalMiddleware{store: store}
	gotSessionID, ok := middleware.autoApproveSessionID(ctx, frame)
	assert.True(t, ok)
	assert.Equal(t, "sess_manager_1", gotSessionID)
	assert.Equal(t, "turn_business", middleware.businessTurnID(ctx, frame))
	assert.Equal(t, "turn_business", frame.businessTurnID)
}

func TestACPSessionIDMiddlewareKeepsOuterIDForPaxdBoundary(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agentModel, err := store.RegisterAgent(ctx, user, domain.RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	session, err := store.CreateNodeAgentSession(
		ctx,
		domain.UserPrincipal{User: user},
		domain.CreateSessionRequest{
			NodeID:    agentModel.NodeID,
			AgentID:   agentModel.AgentID,
			SessionID: "sess_manager_1",
			NativeID:  "harness-session-1",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	agent := &ACPTunnelAgent{
		agentID:     agentModel.AgentID,
		nodeID:      agentModel.NodeID,
		ownerUserID: user.UserID,
		sessionID:   session.SessionID,
	}
	middleware := acpSessionIDMiddleware{store: store}

	userFrame := newACPFrameContext(
		agent,
		acpUserToAgent,
		websocket.TextMessage,
		[]byte(
			`{"jsonrpc":"2.0","method":"session/prompt","params":{"sessionId":"sess_manager_1","prompt":[{"type":"text","text":"hi"}]}}`,
		),
	)
	if err := middleware.HandleACPFrame(ctx, userFrame, func(_ context.Context, frame *acpFrameContext) error {
		assertFrameSessionID(t, frame.payload, "sess_manager_1")
		assert.Equal(t, "harness-session-1", frame.nativeSessionID)
		return nil
	}); err != nil {
		t.Fatalf("user frame middleware: %v", err)
	}

	agentFrame := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		[]byte(
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"harness-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`,
		),
	)
	if err := middleware.HandleACPFrame(ctx, agentFrame, func(_ context.Context, frame *acpFrameContext) error {
		assertFrameSessionID(t, frame.payload, "sess_manager_1")
		return nil
	}); err != nil {
		t.Fatalf("agent frame middleware: %v", err)
	}
}

func TestACPSessionIDMiddlewareUsesPayloadSessionWhenAgentContextEmpty(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agentModel, err := store.RegisterAgent(ctx, user, domain.RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	_, err = store.CreateNodeAgentSession(
		ctx,
		domain.UserPrincipal{User: user},
		domain.CreateSessionRequest{
			NodeID:    agentModel.NodeID,
			AgentID:   agentModel.AgentID,
			SessionID: "sess_manager_1",
			NativeID:  "harness-session-1",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	agent := &ACPTunnelAgent{
		agentID:     agentModel.AgentID,
		nodeID:      agentModel.NodeID,
		ownerUserID: user.UserID,
	}
	middleware := acpSessionIDMiddleware{store: store}

	userFrame := newACPFrameContext(
		agent,
		acpUserToAgent,
		websocket.TextMessage,
		[]byte(
			`{"jsonrpc":"2.0","method":"session/prompt","params":{"sessionId":"sess_manager_1","prompt":[{"type":"text","text":"hi"}]}}`,
		),
	)
	if err := middleware.HandleACPFrame(ctx, userFrame, func(_ context.Context, frame *acpFrameContext) error {
		assertFrameSessionID(t, frame.payload, "sess_manager_1")
		assert.Equal(t, "harness-session-1", frame.nativeSessionID)
		return nil
	}); err != nil {
		t.Fatalf("user frame middleware: %v", err)
	}
}

func TestRewriteACPFrameSessionIDGivenPaxInvocationThenPreservesEndpointSessions(
	t *testing.T,
) {
	payload := []byte(`{
		"jsonrpc":"2.0",
		"id":8,
		"method":"session/prompt",
		"params":{
			"sessionId":"sess_target",
			"prompt":[{"type":"text","text":"review this"}],
			"pax_invocation":{
				"sender":{"agent_id":"agent_source","session_id":"sess_source"},
				"receiver":{"agent_id":"agent_target","session_id":"sess_target"}
			}
		}
	}`)

	rewritten, changed, err := rewriteACPFrameSessionID(payload, "native-target")

	require.NoError(t, err)
	require.True(t, changed)
	var got struct {
		Params struct {
			SessionID     string `json:"sessionId"`
			PaxInvocation struct {
				Sender struct {
					SessionID string `json:"session_id"`
				} `json:"sender"`
				Receiver struct {
					SessionID string `json:"session_id"`
				} `json:"receiver"`
			} `json:"pax_invocation"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(rewritten, &got))
	assert.Equal(t, "native-target", got.Params.SessionID)
	assert.Equal(t, "sess_source", got.Params.PaxInvocation.Sender.SessionID)
	assert.Equal(t, "sess_target", got.Params.PaxInvocation.Receiver.SessionID)
}

func TestACPSessionIDMiddlewareTranslatesAgentFrameWhenTunnelUsesNativeID(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agentModel, err := store.RegisterAgent(ctx, user, domain.RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	session, err := store.CreateNodeAgentSession(
		ctx,
		domain.UserPrincipal{User: user},
		domain.CreateSessionRequest{
			NodeID:    agentModel.NodeID,
			AgentID:   agentModel.AgentID,
			SessionID: "sess_manager_1",
			NativeID:  "harness-session-1",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	agent := &ACPTunnelAgent{
		agentID:     agentModel.AgentID,
		nodeID:      agentModel.NodeID,
		ownerUserID: user.UserID,
		sessionID:   session.NativeID,
	}
	middleware := acpSessionIDMiddleware{store: store}

	agentFrame := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		[]byte(
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"harness-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`,
		),
	)
	if err := middleware.HandleACPFrame(ctx, agentFrame, func(_ context.Context, frame *acpFrameContext) error {
		assertFrameSessionID(t, frame.payload, session.SessionID)
		return nil
	}); err != nil {
		t.Fatalf("agent frame middleware: %v", err)
	}
}

func TestACPSessionLifecycleMiddlewareBindsSessionNewResponse(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(func() time.Time {
		return time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	})
	user, err := store.EnsureUser(ctx, "todd@example.com", "Todd", "user")
	if err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	agentModel, err := store.RegisterAgent(ctx, user, domain.RegisterAgentRequest{
		Name: "agent",
		OS:   "darwin",
	}, "hash")
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	agent := &ACPTunnelAgent{
		agentID:     agentModel.AgentID,
		nodeID:      agentModel.NodeID,
		ownerUserID: user.UserID,
		sessionID:   "sess-manager-1",
		store:       store,
	}
	pipeline := newACPFramePipeline(
		acpSessionLifecycleMiddleware{store: store},
		acpSessionIDMiddleware{store: store},
	)

	requestPayload := []byte(
		`{"jsonrpc":"2.0","id":7,"method":"session/new","params":{"cwd":"/tmp"}}`,
	)
	requestFrame := newACPFrameContext(
		agent,
		acpUserToAgent,
		websocket.TextMessage,
		requestPayload,
	)
	if err := pipeline.Handle(ctx, requestFrame, func(_ context.Context, frame *acpFrameContext) error {
		if string(frame.payload) != string(requestPayload) {
			t.Fatalf("session/new request was rewritten: %s", frame.payload)
		}
		return nil
	}); err != nil {
		t.Fatalf("session/new request pipeline: %v", err)
	}

	responseFrame := newACPFrameContext(
		agent,
		acpAgentToUser,
		websocket.TextMessage,
		[]byte(`{"jsonrpc":"2.0","id":7,"result":{"sessionId":"native-session-1"}}`),
	)
	if err := pipeline.Handle(ctx, responseFrame, func(_ context.Context, frame *acpFrameContext) error {
		assertFrameResultSessionID(t, frame.payload, "sess-manager-1")
		return nil
	}); err != nil {
		t.Fatalf("session/new response pipeline: %v", err)
	}

	sessions, err := store.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: user},
		agentModel.AgentID,
	)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 ||
		sessions[0].SessionID != "sess-manager-1" ||
		sessions[0].NativeID != "native-session-1" {
		t.Fatalf("sessions = %+v, want manager/native mapping", sessions)
	}

	promptFrame := newACPFrameContext(
		agent,
		acpUserToAgent,
		websocket.TextMessage,
		[]byte(
			`{"jsonrpc":"2.0","id":8,"method":"session/prompt","params":{"sessionId":"sess-manager-1","prompt":[{"type":"text","text":"hi"}]}}`,
		),
	)
	if err := pipeline.Handle(ctx, promptFrame, func(_ context.Context, frame *acpFrameContext) error {
		assertFrameSessionID(t, frame.payload, "sess-manager-1")
		assert.Equal(t, "native-session-1", frame.nativeSessionID)
		return nil
	}); err != nil {
		t.Fatalf("prompt pipeline: %v", err)
	}
}

func assertFrameSessionID(t *testing.T, payload []byte, want string) {
	t.Helper()
	var got struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Params.SessionID != want {
		t.Fatalf("frame sessionId = %q, want %q; payload=%s", got.Params.SessionID, want, payload)
	}
}

func assertFrameResultSessionID(t *testing.T, payload []byte, want string) {
	t.Helper()
	var got struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Result.SessionID != want {
		t.Fatalf(
			"frame result sessionId = %q, want %q; payload=%s",
			got.Result.SessionID,
			want,
			payload,
		)
	}
}

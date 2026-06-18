package manager

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
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
		if err := projector.Observe(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
	}

	observe(acpUserToAgent, `{
		"jsonrpc":"2.0",
		"id":1,
		"method":"session/prompt",
		"params":{"sessionId":"sess_1","prompt":[{"type":"text","text":"hi"}]}
	}`)
	state := recorder.last()
	if state.Lifecycle != domain.RuntimeLifecycleRunning ||
		state.ActivePromptRequestID != "1" ||
		state.ActiveTurnID != "1" {
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
		state.ActivePromptRequestID != "" {
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

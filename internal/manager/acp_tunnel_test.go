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
	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/require"
)

func TestACPTunnelHubClaimFallsBackToAgentTunnel(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	hub.add("agent-1", "", agentConn)

	got, err := hub.claim("agent-1", "sess-1")
	if err != nil {
		t.Fatalf("claim session tunnel through agent tunnel: %v", err)
	}
	if got != agentConn {
		t.Fatalf("claim returned %#v, want %#v", got, agentConn)
	}
}

func TestACPTunnelHubClaimPrefersExactSessionTunnel(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	sessionConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "sess-1"}
	hub.add("agent-1", "", agentConn)
	hub.add("agent-1", "sess-1", sessionConn)

	got, err := hub.claim("agent-1", "sess-1")
	if err != nil {
		t.Fatalf("claim exact session tunnel: %v", err)
	}
	if got != sessionConn {
		t.Fatalf("claim returned %#v, want %#v", got, sessionConn)
	}
}

func TestACPTunnelHubClaimAnyTriesNativeBeforeAgentTunnel(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	nativeConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "native-1"}
	hub.add("agent-1", "", agentConn)
	hub.add("agent-1", "native-1", nativeConn)

	got, err := hub.claimAny("agent-1", "sess_manager_1", "native-1", "")
	if err != nil {
		t.Fatalf("claim native session tunnel: %v", err)
	}
	if got != nativeConn {
		t.Fatalf("claim returned %#v, want %#v", got, nativeConn)
	}
}

func TestACPTunnelHubClaimAnyWaitRetriesUntilAgentTunnelReconnects(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1"}
	go func() {
		time.Sleep(20 * time.Millisecond)
		hub.add("agent-1", "", agentConn)
	}()

	got, err := hub.claimAnyWait(
		context.Background(),
		time.Second,
		5*time.Millisecond,
		"agent-1",
		"",
	)

	require.NoError(t, err)
	require.Same(t, agentConn, got)
}

func TestACPTunnelHubBorrowAnyGivenPairedSessionTunnelThenReturnsWithoutReleasingUserPair(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "sess-1"}
	hub.add("agent-1", "sess-1", agentConn)
	state := agentConn.liveState()
	state.mu.Lock()
	state.paired = true
	state.userWS = &websocket.Conn{}
	agentConn.paired = true
	state.mu.Unlock()

	got, release, err := hub.borrowAny("agent-1", "sess-1")
	require.NoError(t, err)
	require.Same(t, agentConn, got)
	release()

	state.mu.Lock()
	defer state.mu.Unlock()
	require.True(t, state.paired)
	require.True(t, agentConn.paired)
}

func TestACPTunnelHubBorrowAnyGivenPairedWithoutUserThenReturnsConflict(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "sess-1"}
	hub.add("agent-1", "sess-1", agentConn)
	state := agentConn.liveState()
	state.mu.Lock()
	state.paired = true
	agentConn.paired = true
	state.mu.Unlock()

	got, release, err := hub.borrowAny("agent-1", "sess-1")
	defer release()

	require.Error(t, err)
	require.Nil(t, got)
}

func TestACPTunnelHubBorrowAnyGivenUnpairedSessionTunnelThenReleaseClearsPair(t *testing.T) {
	hub := NewACPTunnelHub()
	agentConn := &ACPTunnelAgent{agentID: "agent-1", sessionID: "sess-1"}
	hub.add("agent-1", "sess-1", agentConn)

	got, release, err := hub.borrowAny("agent-1", "sess-1")
	require.NoError(t, err)
	require.Same(t, agentConn, got)
	require.True(t, agentConn.paired)

	release()

	require.False(t, agentConn.paired)
}

func TestACPTunnelHubClaimAnyWaitStopsWhenContextIsCanceled(t *testing.T) {
	hub := NewACPTunnelHub()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := hub.claimAnyWait(ctx, time.Second, 5*time.Millisecond, "agent-1", "")

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, got)
}

func TestACPTunnelAgentSessionContextRestoresPreviousSession(t *testing.T) {
	agent := &ACPTunnelAgent{sessionID: ""}

	restore := agent.withSessionContext("sess_1")
	if agent.sessionID != "sess_1" {
		t.Fatalf("sessionID = %q, want temporary context", agent.sessionID)
	}
	restore()
	if agent.sessionID != "" {
		t.Fatalf("sessionID = %q, want restored empty context", agent.sessionID)
	}
}

func TestShouldWarnDroppedACPFrame(t *testing.T) {
	tests := []struct {
		name            string
		frame           acpJSONRPCMessage
		deliveredWaiter bool
		deliveredSSE    bool
		asyncReceivers  acpAsyncReceiverCounts
		wantShouldWarn  bool
	}{
		{
			name: "ignores notification without receiver",
			frame: acpJSONRPCMessage{
				Method: "session/update",
			},
		},
		{
			name: "warns when request or response frame has no receiver",
			frame: acpJSONRPCMessage{
				ID: json.RawMessage(`7`),
			},
			wantShouldWarn: true,
		},
		{
			name: "ignores frame delivered to waiter",
			frame: acpJSONRPCMessage{
				ID: json.RawMessage(`7`),
			},
			deliveredWaiter: true,
		},
		{
			name: "ignores frame delivered to SSE subscriber",
			frame: acpJSONRPCMessage{
				Method: "session/update",
			},
			deliveredSSE: true,
		},
		{
			name: "ignores frame while async receiver exists",
			frame: acpJSONRPCMessage{
				ID: json.RawMessage(`7`),
			},
			asyncReceivers: acpAsyncReceiverCounts{responseWaiters: 1},
		},
		{
			name:           "warns on malformed frame without receiver",
			frame:          acpJSONRPCMessage{},
			wantShouldWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldWarnDroppedACPFrame(
				tt.frame,
				tt.deliveredWaiter,
				tt.deliveredSSE,
				tt.asyncReceivers,
			)

			require.Equal(t, tt.wantShouldWarn, got)
		})
	}
}

func TestJSONRPCRequestDoesNotWakeResponseWaiter(t *testing.T) {
	agent := &ACPTunnelAgent{}
	waiter, cancel := agent.addResponseWaiter("1")
	defer cancel()

	request := acpJSONRPCMessage{
		ID:     json.RawMessage(`1`),
		Method: "session/request_permission",
	}
	delivered := false
	if isACPJSONRPCResponse(request) {
		delivered = agent.notifyResponseWaiter(
			acpJSONRPCID(request),
			[]byte(`{"id":1,"method":"session/request_permission"}`),
		)
	}
	require.False(t, delivered)
	select {
	case payload := <-waiter:
		t.Fatalf("request woke response waiter with payload %s", payload)
	default:
	}

	response := acpJSONRPCMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"stopReason":"end_turn"}`),
	}
	require.True(t, isACPJSONRPCResponse(response))
	require.True(
		t,
		agent.notifyResponseWaiter(
			acpJSONRPCID(response),
			[]byte(`{"id":1,"result":{"stopReason":"end_turn"}}`),
		),
	)
	select {
	case payload := <-waiter:
		require.JSONEq(t, `{"id":1,"result":{"stopReason":"end_turn"}}`, string(payload))
	case <-time.After(time.Second):
		t.Fatal("response did not wake response waiter")
	}
}

func TestACPRequestPermissionAddsAllowAlwaysOption(t *testing.T) {
	params := map[string]any{
		"options": []any{
			map[string]any{"kind": "reject_once", "name": "Reject", "optionId": "reject"},
			map[string]any{"kind": "allow_once", "name": "Allow", "optionId": "allow"},
		},
	}

	if !appendACPAllowAlwaysOption(params) {
		t.Fatal("append option changed = false")
	}
	options, ok := params["options"].([]any)
	if !ok {
		t.Fatalf("options type = %T", params["options"])
	}
	if got := acpOptionID(options[len(options)-1]); got != "allow_always_on_all_agents" {
		t.Fatalf("last option id = %q", got)
	}
	if appendACPAllowAlwaysOption(params) {
		t.Fatal("duplicate allow always option was appended")
	}
}

func TestACPAllowOnceResponseUsesExistingAllowOnceOption(t *testing.T) {
	msg := acpJSONRPCMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`7`),
		Method:  "session/request_permission",
	}
	params := map[string]any{
		"options": []any{
			map[string]any{"kind": "reject_once", "name": "Reject", "optionId": "reject"},
			map[string]any{"kind": "allow_once", "name": "Allow", "optionId": "allow"},
		},
	}

	raw, err := acpAllowOnceResponse(msg, params)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.JSONRPC != "2.0" || response.ID != 7 {
		t.Fatalf("unexpected response frame: %s", raw)
	}
	if response.Result.Outcome.Outcome != "selected" ||
		response.Result.Outcome.OptionID != "allow" {
		t.Fatalf("unexpected permission response outcome: %s", raw)
	}
}

func TestACPPermissionFingerprintUsesStableToolCallFields(t *testing.T) {
	first := map[string]any{
		"sessionId": "session-a",
		"options":   []any{map[string]any{"kind": "allow_once", "optionId": "allow"}},
		"toolCall": map[string]any{
			"toolCallId": "toolu_01first",
			"kind":       "execute",
			"title":      "curl -s https://api.example.com/v1/status",
			"rawInput": map[string]any{
				"command":     "curl -s https://api.example.com/v1/status",
				"description": "Fetch status from example API endpoint",
			},
		},
	}
	second := map[string]any{
		"sessionId": "session-b",
		"options":   []any{map[string]any{"kind": "allow_once", "optionId": "allow"}},
		"toolCall": map[string]any{
			"toolCallId": "toolu_01second",
			"kind":       "execute",
			"title":      "curl -s https://api.example.com/v1/status",
			"rawInput": map[string]any{
				"command":     "curl -s https://api.example.com/v1/status",
				"description": "Fetch status from example API endpoint",
			},
		},
	}

	firstFingerprint, err := acpPermissionFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := acpPermissionFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != "acp:tool_call:execute:curl -s https://api.example.com/v1/status" {
		t.Fatalf("fingerprint = %q", firstFingerprint)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("fingerprints differ: %q != %q", firstFingerprint, secondFingerprint)
	}
}

func TestACPPermissionFingerprintSupportsSnakeCaseToolCall(t *testing.T) {
	first := map[string]any{
		"session_id": "session-a",
		"options":    []any{map[string]any{"kind": "allow_once", "option_id": "allow"}},
		"tool_call": map[string]any{
			"tool_call_id": "toolu_01first",
			"kind":         "execute",
			"raw_input": map[string]any{
				"command": "go test ./...",
			},
		},
	}
	second := map[string]any{
		"session_id": "session-b",
		"options":    []any{map[string]any{"kind": "allow_once", "option_id": "allow"}},
		"tool_call": map[string]any{
			"tool_call_id": "toolu_01second",
			"kind":         "execute",
			"raw_input": map[string]any{
				"command": "go test ./...",
			},
		},
	}

	firstFingerprint, err := acpPermissionFingerprint(first)
	require.NoError(t, err)
	secondFingerprint, err := acpPermissionFingerprint(second)
	require.NoError(t, err)
	require.Equal(t, "acp:tool_call:execute:go test ./...", firstFingerprint)
	require.Equal(t, firstFingerprint, secondFingerprint)
}

func TestACPPermissionFingerprintFallsBackToStableRawInput(t *testing.T) {
	first := map[string]any{
		"sessionId": "session-a",
		"toolCall": map[string]any{
			"toolCallId": "toolu_01first",
			"kind":       "other",
			"rawInput": map[string]any{
				"linkedin_username": "test_user_debug",
			},
		},
	}
	second := map[string]any{
		"sessionId": "session-b",
		"toolCall": map[string]any{
			"toolCallId": "toolu_01second",
			"kind":       "other",
			"rawInput": map[string]any{
				"linkedin_username": "test_user_debug",
			},
		},
	}

	firstFingerprint, err := acpPermissionFingerprint(first)
	require.NoError(t, err)
	secondFingerprint, err := acpPermissionFingerprint(second)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(firstFingerprint, "acp:tool_call:other:raw_input:"))
	require.Equal(t, firstFingerprint, secondFingerprint)
}

func TestRunACPActorsRecoversPanic(t *testing.T) {
	err := runACPActors(context.Background(), acpActor{
		name: "panic_actor",
		run: func(context.Context) error {
			panic("boom")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v, want panic error", err)
	}
}

func TestACPTunnelReconcilePaxdProducerResponse(t *testing.T) {
	agent := &ACPTunnelAgent{}
	tests := []struct {
		name                 string
		request              reliablemq.Envelope
		consumerAckedThrough int64
		wantAction           reliablemq.ReconcileAction
		wantFrom             int64
		wantThrough          int64
		wantAdvanceNext      int64
	}{
		{
			name: "aligned when producer and consumer match",
			request: reliablemq.Envelope{
				Type:            reliablemq.EnvelopeTypeReconcileRequest,
				QueueID:         "queue_1",
				Stream:          reliablemq.StreamACP,
				ProducerNextSeq: 4,
			},
			consumerAckedThrough: 3,
			wantAction:           reliablemq.ReconcileActionAligned,
		},
		{
			name: "replay when consumer is behind and producer has the range",
			request: reliablemq.Envelope{
				Type:            reliablemq.EnvelopeTypeReconcileRequest,
				QueueID:         "queue_1",
				Stream:          reliablemq.StreamACP,
				ProducerNextSeq: 4,
				ReplayFrom:      2,
				ReplayThrough:   3,
			},
			consumerAckedThrough: 1,
			wantAction:           reliablemq.ReconcileActionReplay,
			wantFrom:             2,
			wantThrough:          3,
		},
		{
			name: "advance producer when consumer is ahead",
			request: reliablemq.Envelope{
				Type:            reliablemq.EnvelopeTypeReconcileRequest,
				QueueID:         "queue_1",
				Stream:          reliablemq.StreamACP,
				ProducerNextSeq: 4,
			},
			consumerAckedThrough: 8,
			wantAction:           reliablemq.ReconcileActionAdvanceProducer,
			wantAdvanceNext:      9,
		},
		{
			name: "rotate when consumer is behind missing nonreplayable range",
			request: reliablemq.Envelope{
				Type:            reliablemq.EnvelopeTypeReconcileRequest,
				QueueID:         "queue_1",
				Stream:          reliablemq.StreamACP,
				ProducerNextSeq: 4,
			},
			consumerAckedThrough: 1,
			wantAction:           reliablemq.ReconcileActionRotate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := agent.reconcilePaxdProducerResponse(tt.request, tt.consumerAckedThrough)

			require.Equal(t, tt.wantAction, response.Action)
			require.Equal(t, tt.consumerAckedThrough, response.ConsumerAckedThrough)
			require.Equal(t, tt.wantFrom, response.From)
			require.Equal(t, tt.wantThrough, response.Through)
			require.Equal(t, tt.wantAdvanceNext, response.AdvanceProducerNextSeq)
		})
	}
}

func TestUserTunnelMetadataUsesHeadersAndFallbackTunnelID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/self/agents/a/tunnel", nil)
	req.Header.Set("X-Pax-Client-ID", "client_1")
	req.Header.Set("X-Pax-Device-ID", "device_1")

	got := userTunnelMetadata(req)
	if got.ClientID != "client_1" || got.DeviceID != "device_1" {
		t.Fatalf("metadata = %+v", got)
	}
	if !strings.HasPrefix(got.TunnelID, "log_") {
		t.Fatalf("tunnel id = %q, want generated id", got.TunnelID)
	}
}

func TestUserTunnelMetadataPrefersExplicitTunnelID(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/agents/a/tunnel?client_id=query_client&device_id=query_device&tunnel_id=tunnel_1",
		nil,
	)

	got := userTunnelMetadata(req)
	if got.ClientID != "query_client" ||
		got.DeviceID != "query_device" ||
		got.TunnelID != "tunnel_1" {
		t.Fatalf("metadata = %+v", got)
	}
}

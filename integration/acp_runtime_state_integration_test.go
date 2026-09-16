//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pax-beehive/paxkit/reliablemq"
)

func TestACPTunnelWithAuthoritativeRuntimeSnapshotsIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)

	ownerHeaders := fixture.userHeaders()
	node := createBoundaryNode(t, fixture, ownerHeaders, "acp-runtime-node")
	createdAgent := createBoundaryAgent(t, fixture, ownerHeaders, node.NodeID, "acp-runtime-agent")
	sessionID := "sess-acp-runtime"
	postJSON[session](t, fixture,
		"/api/v1/user/self/nodes/"+node.NodeID+"/agents/"+createdAgent.Agent.AgentID+"/sessions",
		map[string]any{"session_id": sessionID, "native_id": sessionID},
		ownerHeaders, http.StatusOK,
	)
	controlWS, _, err := websocket.DefaultDialer.Dial(
		wsURL(fixture.baseURL, "/api/v1/node/control?node_id="+url.QueryEscape(node.NodeID)),
		http.Header{"X-Pax-Key": []string{node.APIKey}},
	)
	if err != nil {
		t.Fatalf("connect node control tunnel: %v", err)
	}
	defer func() { _ = controlWS.Close() }()
	sequence := 0
	reportRuntime := func(turnID, status, approvalID string) {
		sequence++
		activeTurns := []map[string]any{}
		if status != "idle" {
			activeTurns = append(activeTurns, map[string]any{
				"native_session_id": sessionID, "turn_id": turnID,
				"prompt_request_id": 1, "runtime_status": status,
				"pending_approval_id": approvalID,
			})
		}
		writeRawWS(t, controlWS, mustMarshalString(t, map[string]any{
			"kind": "report", "version": 1,
			"report_id": fmt.Sprintf("runtime-%d", sequence),
			"report": map[string]any{
				"type": "session_runtime.snapshot", "remote_id": "integration-runtime",
				"node_id": node.NodeID, "sent_at": time.Now().UTC(),
				"session_runtime_snapshot": map[string]any{
					"agent_id": createdAgent.Agent.AgentID, "connection_id": "integration-connection",
					"sequence": sequence, "generated_at": time.Now().UTC(),
					"schema_version": 1, "active_turns": activeTurns,
				},
			},
		}))
	}
	assertRuntimeUnchanged := func(want string) {
		t.Helper()
		got := getJSON[session](t, fixture,
			"/api/user/agents/"+createdAgent.Agent.AgentID+"/sessions/"+sessionID,
			ownerHeaders, http.StatusOK,
		)
		if got.RuntimeState == nil || got.RuntimeState.Lifecycle != want {
			t.Fatalf("ACP overwrote snapshot state: got %+v, want %s", got.RuntimeState, want)
		}
	}

	agentWS := fixture.connectACPAgentTunnel(
		t,
		node.APIKey,
		createdAgent.Agent.AgentID,
		sessionID,
	)
	defer func() { _ = agentWS.Close() }()
	userWS := fixture.connectACPUserTunnel(t, createdAgent.Agent.AgentID, sessionID, ownerHeaders)
	defer func() { _ = userWS.Close() }()

	writeRawWS(t, userWS, `{
		"jsonrpc":"2.0",
		"id":1,
		"method":"session/prompt",
		"params":{"sessionId":"sess-acp-runtime","prompt":[{"type":"text","text":"run tests"}]}
	}`)
	prompt := assertAgentDataContains(t, agentWS, 1, `"method":"session/prompt"`)
	turnID := prompt.Metadata["turn_id"]
	if turnID == "" {
		t.Fatal("prompt envelope is missing turn_id")
	}
	reportRuntime(turnID, "running", "")
	waitForRuntimeState(
		t,
		fixture,
		createdAgent.Agent.AgentID,
		sessionID,
		"running",
		func(state *runtimeState) bool {
			return state.ActivePromptRequestID == "1"
		},
	)

	writeAgentData(t, agentWS, createdAgent.Agent.AgentID, 1, `{
		"jsonrpc":"2.0",
		"id":"perm-1",
		"method":"session/request_permission",
		"params":{
			"sessionId":"sess-acp-runtime",
			"toolCall":{
				"toolCallId":"call-1",
				"kind":"execute",
				"title":"go test ./...",
				"status":"pending"
			},
			"options":[{"optionId":"allow","kind":"allow_once"}]
		}
	}`)
	assertAgentAck(t, agentWS, createdAgent.Agent.AgentID, 1)
	assertRawWSContains(t, userWS, `"session/request_permission"`)
	assertRuntimeUnchanged("running")
	reportRuntime(turnID, "waiting_approval", "perm-1")
	waitForRuntimeState(
		t,
		fixture,
		createdAgent.Agent.AgentID,
		sessionID,
		"waiting_approval",
		func(state *runtimeState) bool {
			return state.BlockedReason == "tool_approval" &&
				state.PendingApprovalID == "perm-1"
		},
	)

	writeRawWS(t, userWS, `{
		"jsonrpc":"2.0",
		"id":"perm-1",
		"result":{"optionId":"allow","kind":"allow_once"}
	}`)
	assertAgentDataContains(t, agentWS, 2, `"perm-1"`)
	assertRuntimeUnchanged("waiting_approval")
	reportRuntime(turnID, "running", "")
	waitForRuntimeState(
		t,
		fixture,
		createdAgent.Agent.AgentID,
		sessionID,
		"running",
		func(state *runtimeState) bool {
			return state.BlockedReason == "" && state.PendingApprovalID == ""
		},
	)

	writeAgentData(t, agentWS, createdAgent.Agent.AgentID, 2, `{
		"jsonrpc":"2.0",
		"id":1,
		"result":{"stopReason":"end_turn"}
	}`)
	assertAgentAck(t, agentWS, createdAgent.Agent.AgentID, 2)
	assertRawWSContains(t, userWS, `"stopReason"`)
	assertRuntimeUnchanged("running")
	reportRuntime(turnID, "idle", "")
	waitForRuntimeState(
		t,
		fixture,
		createdAgent.Agent.AgentID,
		sessionID,
		"idle",
		func(state *runtimeState) bool {
			return state.ActivePromptRequestID == "" && state.PendingApprovalID == ""
		},
	)
}

func (f *integrationFixture) connectACPAgentTunnel(
	t *testing.T,
	apiKey string,
	agentID string,
	sessionID string,
) *websocket.Conn {
	t.Helper()
	endpoint := wsURL(
		f.baseURL,
		"/api/v1/agent/tunnel?agent_id="+url.QueryEscape(agentID)+
			"&session_id="+url.QueryEscape(sessionID),
	)
	ws, _, err := websocket.DefaultDialer.Dial(
		endpoint,
		http.Header{"X-Pax-Key": []string{apiKey}},
	)
	if err != nil {
		t.Fatalf("connect ACP agent tunnel: %v", err)
	}
	request, err := reliablemq.MarshalEnvelope(reliablemq.ReconcileRequestEnvelope(
		reliablemq.ProducerReconcileCheckpoint{
			QueueID:         agentID,
			Stream:          reliablemq.StreamACP,
			ProducerNextSeq: 1,
		},
	))
	if err != nil {
		t.Fatalf("marshal reconcile request: %v", err)
	}
	writeRawWS(t, ws, string(request))
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set websocket read deadline: %v", err)
	}
	_, payload, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read reconcile response: %v", err)
	}
	if err := ws.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear websocket read deadline: %v", err)
	}
	response, err := reliablemq.UnmarshalEnvelope(payload)
	if err != nil {
		t.Fatalf("unmarshal reconcile response: %v", err)
	}
	if response.Type != reliablemq.EnvelopeTypeReconcileResponse {
		t.Fatalf("unexpected reconcile response type %q", response.Type)
	}
	return ws
}

func (f *integrationFixture) connectACPUserTunnel(
	t *testing.T,
	agentID string,
	sessionID string,
	headers map[string]string,
) *websocket.Conn {
	t.Helper()
	endpoint := wsURL(
		f.baseURL,
		"/api/v1/user/self/agents/"+agentID+"/sessions/"+sessionID+"/tunnel",
	)
	httpHeaders := http.Header{}
	for k, v := range headers {
		httpHeaders.Set(k, v)
	}
	ws, _, err := websocket.DefaultDialer.Dial(endpoint, httpHeaders)
	if err != nil {
		t.Fatalf("connect ACP user tunnel: %v", err)
	}
	return ws
}

func wsURL(baseURL string, path string) string {
	return "ws" + strings.TrimPrefix(baseURL, "http") + path
}

func writeRawWS(t *testing.T, ws *websocket.Conn, raw string) {
	t.Helper()
	if err := ws.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set websocket write deadline: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatalf("write websocket frame: %v", err)
	}
}

type acpTunnelEnvelope struct {
	Type     string            `json:"type"`
	Metadata map[string]string `json:"metadata,omitempty"`
	QueueID  string            `json:"queue_id,omitempty"`
	Stream   string            `json:"stream"`
	Seq      int64             `json:"seq"`
	Payload  json.RawMessage   `json:"payload,omitempty"`
}

func writeAgentData(t *testing.T, ws *websocket.Conn, queueID string, seq int64, raw string) {
	t.Helper()
	data, err := json.Marshal(acpTunnelEnvelope{
		Type:    "data",
		QueueID: queueID,
		Stream:  "acp",
		Seq:     seq,
		Payload: json.RawMessage(raw),
	})
	if err != nil {
		t.Fatalf("marshal agent data envelope: %v", err)
	}
	writeRawWS(t, ws, string(data))
}

func assertAgentDataContains(
	t *testing.T,
	ws *websocket.Conn,
	seq int64,
	want string,
) acpTunnelEnvelope {
	t.Helper()
	env := readAgentEnvelope(t, ws)
	if env.Type != "data" || env.QueueID == "" || env.Stream != "acp" || env.Seq != seq {
		t.Fatalf("agent envelope = %+v, want data acp seq %d with queue_id", env, seq)
	}
	if !strings.Contains(string(env.Payload), want) {
		t.Fatalf("agent envelope payload %s does not contain %s", env.Payload, want)
	}
	writeRawWS(t, ws, mustMarshalString(t, acpTunnelEnvelope{
		Type:    "ack",
		QueueID: env.QueueID,
		Stream:  "acp",
		Seq:     seq,
	}))
	return env
}

func assertAgentAck(t *testing.T, ws *websocket.Conn, queueID string, seq int64) {
	t.Helper()
	env := readAgentEnvelope(t, ws)
	if env.Type != "ack" || env.QueueID != queueID || env.Stream != "acp" || env.Seq != seq {
		t.Fatalf("agent ack envelope = %+v, want ack acp queue %s seq %d", env, queueID, seq)
	}
}

func readAgentEnvelope(t *testing.T, ws *websocket.Conn) acpTunnelEnvelope {
	t.Helper()
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set agent websocket read deadline: %v", err)
	}
	_, payload, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read agent websocket frame: %v", err)
	}
	var env acpTunnelEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("decode agent websocket envelope %s: %v", payload, err)
	}
	return env
}

func mustMarshalString(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal websocket payload: %v", err)
	}
	return string(data)
}

func assertRawWSContains(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set websocket read deadline: %v", err)
	}
	_, payload, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket frame: %v", err)
	}
	if !strings.Contains(string(payload), want) {
		t.Fatalf("websocket payload %s does not contain %s", payload, want)
	}
}

func waitForRuntimeState(
	t *testing.T,
	fixture *integrationFixture,
	agentID string,
	sessionID string,
	lifecycle string,
	match func(*runtimeState) bool,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := getJSON[session](
			t,
			fixture,
			"/api/user/agents/"+agentID+"/sessions/"+sessionID,
			fixture.userHeaders(),
			http.StatusOK,
		)
		if got.RuntimeState != nil &&
			got.RuntimeState.Lifecycle == lifecycle &&
			match(got.RuntimeState) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := getJSON[session](
		t,
		fixture,
		"/api/user/agents/"+agentID+"/sessions/"+sessionID,
		fixture.userHeaders(),
		http.StatusOK,
	)
	t.Fatalf("runtime state did not reach %s: %+v", lifecycle, got.RuntimeState)
}

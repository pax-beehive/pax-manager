//go:build integration

package integration_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestACPTunnelRuntimeStateIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)

	ownerHeaders := fixture.userHeaders()
	node := createBoundaryNode(t, fixture, ownerHeaders, "acp-runtime-node")
	createdAgent := createBoundaryAgent(t, fixture, ownerHeaders, node.NodeID, "acp-runtime-agent")
	sessionID := "sess-acp-runtime"
	createBoundarySession(
		t,
		fixture,
		ownerHeaders,
		node.NodeID,
		createdAgent.Agent.AgentID,
		sessionID,
	)

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
	assertRawWSContains(t, agentWS, `"method":"session/prompt"`)
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

	writeRawWS(t, agentWS, `{
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
	assertRawWSContains(t, userWS, `"session/request_permission"`)
	waitForRuntimeState(
		t,
		fixture,
		createdAgent.Agent.AgentID,
		sessionID,
		"waiting_approval",
		func(state *runtimeState) bool {
			return state.BlockedReason == "tool_approval" &&
				state.PendingApprovalID == "perm-1" &&
				len(state.ActiveToolCalls) == 1 &&
				state.ActiveToolCalls[0].ToolCallID == "call-1"
		},
	)

	writeRawWS(t, userWS, `{
		"jsonrpc":"2.0",
		"id":"perm-1",
		"result":{"optionId":"allow","kind":"allow_once"}
	}`)
	assertRawWSContains(t, agentWS, `"perm-1"`)
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

	writeRawWS(t, agentWS, `{
		"jsonrpc":"2.0",
		"id":1,
		"result":{"stopReason":"end_turn"}
	}`)
	assertRawWSContains(t, userWS, `"stopReason"`)
	waitForRuntimeState(
		t,
		fixture,
		createdAgent.Agent.AgentID,
		sessionID,
		"idle",
		func(state *runtimeState) bool {
			return state.LastStopReason == "end_turn"
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

package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationCreatesOpenClawSessionWithoutPerSessionMCPServers(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com", "openclaw")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleConversation)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+fixture.agentID,
		http.Header{"X-Pax-Key": []string{fixture.nodeAPIKey}},
	)
	require.NoError(t, err)
	defer func() { _ = agentWS.Close() }()
	completeMockAgentReconcile(t, agentWS, fixture.agentID, 1)
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go postConversation(t, httpServer.URL, fixture, `{"initialize_only":true}`, respCh, errCh)

	sessionNewEnv := readNextManagerToAgentData(t, agentWS)
	assertACPMethod(t, sessionNewEnv.Payload, "session/new")
	var request struct {
		Params struct {
			MCPServers []json.RawMessage `json:"mcpServers"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(sessionNewEnv.Payload, &request))
	assert.NotNil(t, request.Params.MCPServers)
	assert.Empty(t, request.Params.MCPServers, "OpenClaw rejects per-session MCP servers")
	writeAgentDataFrame(
		t,
		agentWS,
		sessionNewEnv.QueueID,
		1,
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"native-openclaw"}}`),
	)

	body := readConversationResponse(t, respCh, errCh, http.StatusOK)
	events := decodeConversationEvents(t, body)
	sessionEvent := requireConversationEvent(t, events, "session")
	requireConversationEvent(t, events, "done")
	stored, err := srv.store.GetSession(
		t.Context(), testUserPrincipal(t, srv, fixture.userEmail), sessionEvent.SessionID,
	)
	require.NoError(t, err)
	assert.Equal(t, "openclaw", stored.AgentType)
	assert.Equal(t, "native-openclaw", stored.NativeID)
}

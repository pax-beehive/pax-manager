package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryGivenOwnedMCPApprovalThenContinuesManualSessionAndCompletes(t *testing.T) {
	for _, test := range []struct {
		name   string
		style  string
		idOnly bool
	}{
		{name: "DSCODE complete tool request", style: "dscode"},
		{name: "DSCODE tool ID only request", style: "dscode", idOnly: true},
		{name: "Codex structured execute tool request", style: "codex"},
		{name: "Codex tool ID only request", style: "codex", idOnly: true},
		{name: "Hermes tool ID only request", style: "hermes", idOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, _ := testServer(t, "mcp-secretary@example.test")
			fixture := testNodeAgent(t, srv, "mcp-secretary@example.test")
			principal := testUserPrincipal(t, srv, fixture.userEmail)
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
			request := secretary.ExecutionRequest{
				OwnerID: principal.User.UserID, AgentID: fixture.agentID,
				RunID: "owned-mcp-approval", Prompt: "Read the existing summaries, then summarize.",
			}
			type outcome struct {
				result secretary.ExecutionResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, runErr := srv.secretaryExecutor().Execute(t.Context(), request)
				done <- outcome{result: result, err: runErr}
			}()
			created, createRequest := readMockACPRequest(t, ws, "session/new")
			writeMockACPResponse(t, ws, created.QueueID, 1, createRequest.ID,
				json.RawMessage(`{"sessionId":"native-owned-mcp-secretary"}`))
			promptEnvelope, prompt := readMockACPRequest(t, ws, "session/prompt")
			sessionID := secretary.ExecutionSessionID(principal.User.UserID, request.RunID)
			tool := map[string]any{
				"toolCallId": "read-existing-summaries", "kind": "other", "status": "pending",
				"title": "mcp__" + agentConversationMCPServerName(
					sessionID,
				) + "__workspace_summaries",
				"rawInput": map[string]any{"session_id": "source-session", "limit": 1},
			}
			serverName := agentConversationMCPServerName(sessionID)
			switch test.style {
			case "codex":
				tool["title"] = "mcp." + serverName + ".workspace_summaries"
				tool["kind"] = "execute"
				tool["rawInput"] = map[string]any{
					"server": serverName, "tool": "workspace_summaries",
					"arguments": map[string]any{"session_id": "source-session", "limit": 1},
				}
			case "hermes":
				tool["title"] = "mcp_" + strings.ReplaceAll(
					serverName,
					"-",
					"_",
				) + "_workspace_summaries"
			}
			update, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{"sessionId": sessionID, "update": map[string]any{
					"sessionUpdate": "tool_call", "toolCallId": tool["toolCallId"], "kind": tool["kind"],
					"title": tool["title"], "status": "pending", "rawInput": tool["rawInput"],
				}},
			})
			require.NoError(t, err)
			writeAgentDataFrame(t, ws, promptEnvelope.QueueID, 2, update)
			permissionTool := tool
			if test.idOnly {
				permissionTool = map[string]any{"toolCallId": tool["toolCallId"]}
			}
			permission, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": "permission-read-summaries", "method": "session/request_permission",
				"params": map[string]any{"sessionId": sessionID, "toolCall": permissionTool,
					"options": []map[string]string{
						{
							"optionId": "native-allow-this-call",
							"kind":     "allow_once",
							"name":     "Allow once",
						},
						{"optionId": "native-reject", "kind": "reject_once", "name": "Reject"},
					},
				},
			})
			require.NoError(t, err)
			writeAgentDataFrame(t, ws, promptEnvelope.QueueID, 3, permission)
			var endedEarly *outcome
			require.Eventually(t, func() bool {
				select {
				case got := <-done:
					endedEarly = &got
					return true
				default:
				}
				approvals, listErr := srv.store.ListApprovals(t.Context(), domain.ApprovalFilter{
					Principal: principal, RequestSessionID: sessionID, Decision: "allow", Limit: 10,
				})
				return listErr == nil && len(approvals) == 1
			}, 2*time.Second, 10*time.Millisecond)
			require.Nil(
				t,
				endedEarly,
				"Secretary must keep waiting for output after approving its own MCP tool",
			)
			response := readNextManagerToAgentData(t, ws)
			writeMockAgentAck(t, ws, response)
			var decision struct {
				ID     string `json:"id"`
				Result struct {
					Outcome struct {
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(response.Payload, &decision))
			assert.Equal(t, "permission-read-summaries", decision.ID)
			assert.Equal(t, "native-allow-this-call", decision.Result.Outcome.OptionID)
			writeMockACPChunk(
				t,
				ws,
				promptEnvelope.QueueID,
				4,
				sessionID,
				`{"body":"Summary after MCP lookup."}`,
			)
			writeMockACPResponse(
				t,
				ws,
				promptEnvelope.QueueID,
				5,
				prompt.ID,
				json.RawMessage(`{"stopReason":"end_turn"}`),
			)
			select {
			case got := <-done:
				require.NoError(t, got.err)
				assert.JSONEq(t, `{"body":"Summary after MCP lookup."}`, got.result.Text)
			case <-time.After(5 * time.Second):
				t.Fatal("Secretary failed to finish after the automatic MCP approval")
			}
			session, err := srv.store.GetSession(t.Context(), principal, sessionID)
			require.NoError(t, err)
			assert.Equal(t, domain.SessionApprovalModeManual, session.PaxConfig.ApprovalMode)
			assert.Equal(t, domain.RuntimeLifecycleIdle,
				testACPRuntimeState(srv.acpRuntime, fixture.agentID, sessionID).Lifecycle)
		})
	}
}

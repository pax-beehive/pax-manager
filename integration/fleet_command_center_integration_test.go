//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const defaultBaseURL = "http://localhost:9879"

type apiEnvelope[T any] struct {
	Data    T      `json:"data"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type registrationTokenResponse struct {
	Token       string `json:"token"`
	OwnerUserID string `json:"owner_user_id"`
}

type registerAgentResponse struct {
	AgentID string `json:"agent_id"`
	APIKey  string `json:"api_key"`
}

type agent struct {
	AgentID     string `json:"agent_id"`
	OwnerUserID string `json:"owner_user_id"`
	Hostname    string `json:"hostname"`
	AgentType   string `json:"agent_type"`
	Status      string `json:"status"`
}

type agentListResponse struct {
	Agents []agent `json:"agents"`
}

type session struct {
	AgentID        string   `json:"agent_id"`
	SessionID      string   `json:"session_id"`
	SessionName    string   `json:"name"`
	AgentType      string   `json:"agent_type"`
	Status         string   `json:"status"`
	CurrentTask    string   `json:"current_task"`
	MessageCount   int      `json:"message_count"`
	TokenTotal     int64    `json:"token_total"`
	WorkspaceRoots []string `json:"workspace_roots"`
}

type sessionListResponse struct {
	Sessions []session `json:"sessions"`
}

type mailboxMessage struct {
	ID              int64           `json:"id"`
	MessageID       string          `json:"message_id"`
	AgentID         string          `json:"agent_id"`
	SessionID       string          `json:"session_id"`
	Message         string          `json:"message"`
	MessageType     string          `json:"message_type"`
	Payload         json.RawMessage `json:"payload"`
	Status          string          `json:"status"`
	Result          string          `json:"result"`
	Direction       string          `json:"direction"`
	ParentMessageID string          `json:"parent_message_id"`
}

type mailboxListResponse struct {
	Messages []mailboxMessage `json:"messages"`
}

type mailboxPullResponse struct {
	Messages  []mailboxMessage `json:"messages"`
	MaxOffset int64            `json:"max_offset"`
	HasMore   bool             `json:"has_more"`
}

type userAPIKey struct {
	KeyID  string `json:"key_id"`
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
}

type createUserAPIKeyResponse struct {
	APIKey userAPIKey `json:"api_key"`
	Key    string     `json:"key"`
}

type wsResponse struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
	Code      int             `json:"code"`
	Message   string          `json:"message"`
}

type integrationFixture struct {
	t       *testing.T
	baseURL string
	client  *http.Client
	email   string
}

func TestFleetCommandCenterIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)

	var token registrationTokenResponse
	var registered registerAgentResponse
	httpSessionID := "sess-http"
	wsSessionID := "sess-ws"
	var httpMessage mailboxMessage
	var wsMessage mailboxMessage
	var httpMaxOffset int64
	var wsMaxOffset int64

	t.Run(
		"Given the Docker service is running when checking service metadata then health and OpenAPI are available",
		func(t *testing.T) {
			fixture.waitForHealth(t)
			health := fixture.get(t, "/health", nil, http.StatusOK)
			assertEnvelopeCode(t, health, http.StatusOK)

			openAPI := fixture.getRaw(t, "/openapi.json", http.StatusOK)
			if !bytes.Contains(openAPI, []byte(`"/api/v1/user/{user_id}/nodes"`)) {
				t.Fatalf("openapi document does not include user nodes route")
			}
			if !bytes.Contains(openAPI, []byte(`"/api/v1/node/messages/outbound"`)) {
				t.Fatalf("openapi document does not include node outbound route")
			}
		},
	)

	t.Run(
		"Given a new user identity when calling user APIs then the user is registered and can manage platform keys",
		func(t *testing.T) {
			agents := fixture.listAgents(t)
			if len(agents.Agents) != 0 {
				t.Fatalf("new user should not see agents, got %+v", agents.Agents)
			}

			created := postJSON[createUserAPIKeyResponse](
				t,
				fixture,
				"/api/user/api-keys",
				map[string]any{"name": "integration automation"},
				fixture.userHeaders(),
				http.StatusOK,
			)
			if created.APIKey.KeyID == "" || created.Key == "" || created.APIKey.Prefix == "" {
				t.Fatalf("bad user api key response: %+v", created)
			}

			listed := getJSON[struct {
				Keys []userAPIKey `json:"api_keys"`
			}](t, fixture, "/api/user/api-keys", fixture.userHeaders(), http.StatusOK)
			if !hasUserAPIKey(listed.Keys, created.APIKey.KeyID) {
				t.Fatalf("created api key not listed: %+v", listed.Keys)
			}

			fixture.delete(
				t,
				"/api/user/api-keys/"+created.APIKey.KeyID,
				fixture.userHeaders(),
				http.StatusOK,
			)
		},
	)

	t.Run(
		"Given a registered user when minting an agent registration token then paxd can register under that tenant",
		func(t *testing.T) {
			token = postJSON[registrationTokenResponse](
				t,
				fixture,
				"/api/user/agent-registration-tokens",
				map[string]any{"expires_in_seconds": 600},
				fixture.userHeaders(),
				http.StatusOK,
			)
			if token.Token == "" || token.OwnerUserID == "" {
				t.Fatalf("bad registration token response: %+v", token)
			}

			registered = postJSON[registerAgentResponse](
				t,
				fixture,
				"/api/agent/register",
				map[string]any{
					"name":         "integration-agent",
					"hostname":     "integration-host",
					"agent_type":   "hermes",
					"machine_type": "local-docker",
					"os":           "linux",
				},
				map[string]string{"X-Registration-Token": token.Token},
				http.StatusOK,
			)
			if registered.AgentID == "" || registered.APIKey == "" {
				t.Fatalf("bad agent registration response: %+v", registered)
			}
		},
	)

	t.Run(
		"Given a registered paxd when reporting status over HTTP then the user can inspect agents and sessions",
		func(t *testing.T) {
			postJSON[map[string]bool](
				t,
				fixture,
				"/api/agent/status",
				statusReport(registered.AgentID, httpSessionID),
				fixture.agentHeaders(registered.APIKey),
				http.StatusOK,
			)

			agents := fixture.listAgents(t)
			if len(agents.Agents) != 1 || agents.Agents[0].AgentID != registered.AgentID {
				t.Fatalf("unexpected agent list: %+v", agents.Agents)
			}

			agent := getJSON[agent](
				t,
				fixture,
				"/api/user/agents/"+registered.AgentID,
				fixture.userHeaders(),
				http.StatusOK,
			)
			if agent.AgentID != registered.AgentID || agent.Hostname != "integration-host" {
				t.Fatalf("unexpected agent: %+v", agent)
			}

			sessions := getJSON[sessionListResponse](
				t,
				fixture,
				"/api/user/agents/"+registered.AgentID+"/sessions",
				fixture.userHeaders(),
				http.StatusOK,
			)
			assertSession(t, sessions.Sessions, httpSessionID, "running")

			gotSession := getJSON[session](
				t,
				fixture,
				"/api/user/agents/"+registered.AgentID+"/sessions/"+httpSessionID,
				fixture.userHeaders(),
				http.StatusOK,
			)
			if gotSession.SessionID != httpSessionID || gotSession.TokenTotal != 321 {
				t.Fatalf("unexpected session: %+v", gotSession)
			}
		},
	)

	t.Run(
		"Given a user mailbox command when paxd pulls and reports result over HTTP then the user sees completion",
		func(t *testing.T) {
			httpMessage = fixture.createMailboxMessage(
				t,
				registered.AgentID,
				httpSessionID,
				"run http tests",
			)
			sessionMessages := fixture.listSessionMessages(t, registered.AgentID, httpSessionID)
			assertMailboxMessage(t, sessionMessages.Messages, httpMessage.MessageID, "pending", "")

			pull := getJSON[mailboxPullResponse](
				t,
				fixture,
				"/api/agent/sessions/"+httpSessionID+"/mailbox?offset=0&limit=10",
				fixture.agentHeaders(registered.APIKey),
				http.StatusOK,
			)
			assertMailboxMessage(t, pull.Messages, httpMessage.MessageID, "delivered", "")
			httpMaxOffset = pull.MaxOffset

			postJSON[map[string]bool](
				t,
				fixture,
				"/api/agent/messages/"+httpMessage.MessageID+"/result",
				map[string]any{"status": "completed", "result": "http path passed"},
				fixture.agentHeaders(registered.APIKey),
				http.StatusOK,
			)
			postJSON[map[string]bool](
				t,
				fixture,
				"/api/agent/messages/offset",
				map[string]any{"offset": httpMaxOffset},
				fixture.agentHeaders(registered.APIKey),
				http.StatusOK,
			)

			completed := fixture.listSessionMessages(t, registered.AgentID, httpSessionID)
			assertMailboxMessage(
				t,
				completed.Messages,
				httpMessage.MessageID,
				"completed",
				"http path passed",
			)
		},
	)

	t.Run(
		"Given a connected paxd websocket when status and mailbox frames are exchanged then the user sees websocket completion",
		func(t *testing.T) {
			ws := fixture.connectAgentWebsocket(t, registered, wsSessionID)
			defer func() { _ = ws.Close() }()

			connected := readWS(t, ws)
			if connected.Type != "connected" || connected.Code != http.StatusOK {
				t.Fatalf("unexpected websocket connected frame: %+v", connected)
			}

			writeWS(t, ws, "status", "status-1", statusReport(registered.AgentID, wsSessionID))
			assertWSResult(t, readWS(t, ws), "status_result", "status-1")

			wsMessage = fixture.createMailboxMessage(
				t,
				registered.AgentID,
				wsSessionID,
				"run websocket tests",
			)
			writeWS(t, ws, "pull_mailbox", "pull-1", map[string]any{"offset": 0, "limit": 10})
			pull := decodeWSData[mailboxPullResponse](
				t,
				readWS(t, ws),
				"pull_mailbox_result",
				"pull-1",
			)
			assertMailboxMessage(t, pull.Messages, wsMessage.MessageID, "delivered", "")
			wsMaxOffset = pull.MaxOffset

			writeWS(
				t,
				ws,
				"message_result",
				"result-1",
				map[string]any{
					"message_id": wsMessage.MessageID,
					"status":     "completed",
					"result":     "websocket path passed",
				},
			)
			assertWSResult(t, readWS(t, ws), "message_result_result", "result-1")

			writeWS(t, ws, "update_offset", "offset-1", map[string]any{"offset": wsMaxOffset})
			assertWSResult(t, readWS(t, ws), "update_offset_result", "offset-1")

			completed := fixture.listSessionMessages(t, registered.AgentID, wsSessionID)
			assertMailboxMessage(
				t,
				completed.Messages,
				wsMessage.MessageID,
				"completed",
				"websocket path passed",
			)
		},
	)

	t.Run(
		"Given another tenant when accessing the registered agent then tenant boundaries are enforced",
		func(t *testing.T) {
			other := "other-" + fixture.email
			agents := getJSON[agentListResponse](
				t,
				fixture,
				"/api/user/agents",
				map[string]string{"X-User-Email": other},
				http.StatusOK,
			)
			if len(agents.Agents) != 0 {
				t.Fatalf("other tenant saw agents: %+v", agents.Agents)
			}

			fixture.postExpectError(
				t,
				"/api/user/agents/"+registered.AgentID+"/sessions/"+httpSessionID+"/messages",
				map[string]any{
					"session_id":   httpSessionID,
					"message":      "cross tenant",
					"message_type": "chat",
				},
				map[string]string{"X-User-Email": other},
				http.StatusNotFound,
			)
		},
	)
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	baseURL := strings.TrimRight(os.Getenv("INTEGRATION_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &integrationFixture{
		t:       t,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
		email:   fmt.Sprintf("itest-%d@example.com", time.Now().UnixNano()),
	}
}

func (f *integrationFixture) waitForHealth(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := f.client.Get(f.baseURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("service did not become healthy at %s", f.baseURL)
}

func (f *integrationFixture) userHeaders() map[string]string {
	return map[string]string{"X-User-Email": f.email}
}

func (f *integrationFixture) agentHeaders(apiKey string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + apiKey}
}

func (f *integrationFixture) listAgents(t *testing.T) agentListResponse {
	t.Helper()
	return getJSON[agentListResponse](t, f, "/api/user/agents", f.userHeaders(), http.StatusOK)
}

func (f *integrationFixture) createMailboxMessage(
	t *testing.T,
	agentID string,
	sessionID string,
	message string,
) mailboxMessage {
	t.Helper()
	return postJSON[mailboxMessage](
		t,
		f,
		"/api/user/agents/"+agentID+"/sessions/"+sessionID+"/messages",
		map[string]any{
			"message":      message,
			"message_type": "chat",
		},
		f.userHeaders(),
		http.StatusOK,
	)
}

func (f *integrationFixture) listSessionMessages(
	t *testing.T,
	agentID string,
	sessionID string,
) mailboxListResponse {
	t.Helper()
	return getJSON[mailboxListResponse](
		t,
		f,
		"/api/user/agents/"+agentID+"/sessions/"+sessionID+"/messages",
		f.userHeaders(),
		http.StatusOK,
	)
}

func (f *integrationFixture) connectAgentWebsocket(
	t *testing.T,
	registered registerAgentResponse,
	sessionID string,
) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(f.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	endpoint := fmt.Sprintf(
		"%s/api/agent/ws?agent_id=%s&session_id=%s",
		wsURL,
		url.QueryEscape(registered.AgentID),
		url.QueryEscape(sessionID),
	)
	headers := http.Header{"X-Pax-Key": []string{registered.APIKey}}
	ws, _, err := websocket.DefaultDialer.Dial(endpoint, headers)
	if err != nil {
		t.Fatalf("connect websocket: %v", err)
	}
	return ws
}

func (f *integrationFixture) get(
	t *testing.T,
	path string,
	headers map[string]string,
	wantStatus int,
) apiEnvelope[json.RawMessage] {
	t.Helper()
	return decodeEnvelope[json.RawMessage](t, f.getRaw(t, path, wantStatus, headers))
}

func (f *integrationFixture) getRaw(
	t *testing.T,
	path string,
	wantStatus int,
	headers ...map[string]string,
) []byte {
	t.Helper()
	req := f.newRequest(t, http.MethodGet, path, nil)
	if len(headers) > 0 {
		addHeaders(req, headers[0])
	}
	return f.do(t, req, wantStatus)
}

func getJSON[T any](
	t *testing.T,
	f *integrationFixture,
	path string,
	headers map[string]string,
	wantStatus int,
) T {
	t.Helper()
	return decodeEnvelope[T](t, f.getRaw(t, path, wantStatus, headers)).Data
}

func postJSON[T any](
	t *testing.T,
	f *integrationFixture,
	path string,
	body any,
	headers map[string]string,
	wantStatus int,
) T {
	t.Helper()
	req := f.newRequest(t, http.MethodPost, path, body)
	addHeaders(req, headers)
	return decodeEnvelope[T](t, f.do(t, req, wantStatus)).Data
}

func (f *integrationFixture) postExpectError(
	t *testing.T,
	path string,
	body any,
	headers map[string]string,
	wantStatus int,
) {
	t.Helper()
	req := f.newRequest(t, http.MethodPost, path, body)
	addHeaders(req, headers)
	raw := f.do(t, req, wantStatus)
	envelope := decodeEnvelope[json.RawMessage](t, raw)
	if envelope.Code != wantStatus {
		t.Fatalf("error envelope code = %d, want %d: %s", envelope.Code, wantStatus, raw)
	}
}

func (f *integrationFixture) delete(
	t *testing.T,
	path string,
	headers map[string]string,
	wantStatus int,
) {
	t.Helper()
	req := f.newRequest(t, http.MethodDelete, path, nil)
	addHeaders(req, headers)
	_ = f.do(t, req, wantStatus)
}

func (f *integrationFixture) newRequest(
	t *testing.T,
	method string,
	path string,
	body any,
) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(
		context.Background(),
		method,
		f.baseURL+path,
		reader,
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (f *integrationFixture) do(t *testing.T, req *http.Request, wantStatus int) []byte {
	t.Helper()
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw := readAll(t, resp)
	if resp.StatusCode != wantStatus {
		t.Fatalf(
			"%s %s status = %d, want %d, body = %s",
			req.Method,
			req.URL,
			resp.StatusCode,
			wantStatus,
			raw,
		)
	}
	return raw
}

func addHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return buf.Bytes()
}

func decodeEnvelope[T any](t *testing.T, raw []byte) apiEnvelope[T] {
	t.Helper()
	var envelope apiEnvelope[T]
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v: %s", err, raw)
	}
	return envelope
}

func assertEnvelopeCode(t *testing.T, envelope apiEnvelope[json.RawMessage], want int) {
	t.Helper()
	if envelope.Code != want || envelope.Message != "ok" {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func statusReport(agentID string, sessionID string) map[string]any {
	return map[string]any{
		"agent_id": agentID,
		"hostname": "integration-host",
		"sessions": []map[string]any{
			{
				"session_id":      sessionID,
				"agent_type":      "hermes",
				"native_id":       "native-" + sessionID,
				"name":            "integration session",
				"project_id":      "pax-manager",
				"preview":         "integration flow",
				"workspace_roots": []string{"/workspace/pax-manager"},
				"status":          "running",
				"current_task":    "exercise fleet command center protocol",
				"message_count":   2,
				"token_usage":     map[string]any{"total_tokens": 321},
				"model":           "integration-model",
				"run_id":          "run-" + sessionID,
				"run_status":      "running",
			},
		},
	}
}

func assertSession(t *testing.T, sessions []session, sessionID string, status string) {
	t.Helper()
	for _, got := range sessions {
		if got.SessionID == sessionID {
			if got.Status != status || got.TokenTotal != 321 {
				t.Fatalf("unexpected session: %+v", got)
			}
			return
		}
	}
	t.Fatalf("session %s not found in %+v", sessionID, sessions)
}

func assertMailboxMessage(
	t *testing.T,
	messages []mailboxMessage,
	messageID string,
	status string,
	result string,
) {
	t.Helper()
	for _, got := range messages {
		if got.MessageID == messageID {
			if got.Status != status || got.Result != result {
				t.Fatalf("unexpected mailbox message: %+v", got)
			}
			return
		}
	}
	t.Fatalf("message %s not found in %+v", messageID, messages)
}

func hasUserAPIKey(keys []userAPIKey, keyID string) bool {
	for _, key := range keys {
		if key.KeyID == keyID {
			return true
		}
	}
	return false
}

func writeWS(t *testing.T, ws *websocket.Conn, frameType string, requestID string, data any) {
	t.Helper()
	frame := map[string]any{
		"type":       frameType,
		"request_id": requestID,
		"data":       data,
	}
	if err := ws.WriteJSON(frame); err != nil {
		t.Fatalf("write websocket frame: %v", err)
	}
}

func readWS(t *testing.T, ws *websocket.Conn) wsResponse {
	t.Helper()
	if err := ws.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set websocket read deadline: %v", err)
	}
	var resp wsResponse
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatalf("read websocket frame: %v", err)
	}
	return resp
}

func decodeWSData[T any](
	t *testing.T,
	resp wsResponse,
	responseType string,
	requestID string,
) T {
	t.Helper()
	assertWSResult(t, resp, responseType, requestID)
	var out T
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode websocket data: %v: %s", err, resp.Data)
	}
	return out
}

func assertWSResult(t *testing.T, resp wsResponse, responseType string, requestID string) {
	t.Helper()
	if resp.Type != responseType || resp.RequestID != requestID || resp.Code != http.StatusOK {
		t.Fatalf("unexpected websocket response: %+v", resp)
	}
}

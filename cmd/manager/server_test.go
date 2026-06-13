package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestAgentStatusUsesPaxdSessionShape(t *testing.T) {
	srv, apiKey := testServer(t, "todd@example.com")

	statusBody := []byte(`{
		"hostname":"workstation",
		"sessions":[{
			"sessionId":"sess-1",
			"agentType":"hermes",
			"nativeId":"resp-1",
			"name":"repo task",
			"projectId":"repo",
			"preview":"fix tests",
			"workspaceRoots":["/workspace/repo"],
			"status":"running",
			"currentTask":"go test ./...",
			"messageCount":7,
			"tokenUsage":123,
			"model":"gpt-test",
			"runId":"run-1",
			"runStatus":"running"
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/status", bytes.NewReader(statusBody))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/agents/"+testAgentID(t, srv, "todd@example.com")+"/sessions", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Sessions []AgentSession `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions len = %d", len(got.Sessions))
	}
	session := got.Sessions[0]
	if session.SessionID != "sess-1" || session.AgentType != "hermes" || session.TokenTotal != 123 {
		t.Fatalf("unexpected session: %+v", session)
	}
	if len(session.WorkspaceRoots) != 1 || session.WorkspaceRoots[0] != "/workspace/repo" {
		t.Fatalf("workspace roots = %#v", session.WorkspaceRoots)
	}
}

func TestMailboxLifecycle(t *testing.T) {
	srv, apiKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	body := []byte(`{
		"agentId":"` + agentID + `",
		"sessionId":"sess-1",
		"message":"run the tests",
		"messageType":"chat"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/user/message", bytes.NewReader(body))
	req.Header.Set("Cf-Access-Authenticated-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create message code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var created MailboxMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Payload == nil || !json.Valid(created.Payload) {
		t.Fatalf("payload is not valid JSON: %s", created.Payload)
	}
	assertPayloadField(t, created.Payload, "entity_type", "turn")
	assertPayloadField(t, created.Payload, "event_type", "start")
	assertPayloadField(t, created.Payload, "prompt", "run the tests")

	req = httptest.NewRequest(http.MethodGet, "/api/agent/mailbox?offset=0&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var pull MailboxPull
	if err := json.Unmarshal(rec.Body.Bytes(), &pull); err != nil {
		t.Fatal(err)
	}
	if len(pull.Messages) != 1 {
		t.Fatalf("pull messages len = %d", len(pull.Messages))
	}
	if pull.Messages[0].Status != "delivered" || pull.MaxOffset != pull.Messages[0].ID {
		t.Fatalf("unexpected pull: %+v", pull)
	}

	resultBody := []byte(`{"status":"completed","result":"tests passed"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/agent/messages/"+created.MessageID+"/result", bytes.NewReader(resultBody))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("result code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/agent/messages/offset", bytes.NewReader([]byte(`{"offset":`+int64String(pull.MaxOffset)+`}`)))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("offset code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/mailbox?status=completed", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mailbox code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var mailbox struct {
		Messages []MailboxMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mailbox); err != nil {
		t.Fatal(err)
	}
	if len(mailbox.Messages) != 1 || mailbox.Messages[0].Result != "tests passed" {
		t.Fatalf("unexpected mailbox: %+v", mailbox.Messages)
	}
}

func TestTenantIsolationAndAdminBypass(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "ellen@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ellen agents code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 0 {
		t.Fatalf("ellen saw agents: %+v", got.Agents)
	}

	body := []byte(`{"agentId":"` + agentID + `","message":"cross tenant","messageType":"chat"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/user/message", bytes.NewReader(body))
	req.Header.Set("Cf-Access-Authenticated-User-Email", "ellen@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant send code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "admin@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin agents code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("admin agents len = %d", len(got.Agents))
	}
}

func TestUserIdentityRequiresCloudflareHeaderByDefault(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
	srv := newServer(Config{
		LocalUserID: "local@example.com",
		AdminEmails: map[string]bool{"admin@example.com": true},
	}, NewMemoryStore(now))
	srv.clock = now

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", "admin@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed local header code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "admin@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cloudflare header code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminStatusFollowsCurrentConfig(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
	store := NewMemoryStore(now)
	srv := newServer(Config{
		AdminEmails: map[string]bool{"admin@example.com": true},
	}, store)
	srv.clock = now

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "admin@example.com")
	if _, err := srv.userPrincipal(req); err != nil {
		t.Fatal(err)
	}

	srv.cfg.AdminEmails = map[string]bool{}
	principal, err := srv.userPrincipal(req)
	if err != nil {
		t.Fatal(err)
	}
	if principal.IsAdmin {
		t.Fatal("admin status persisted after removal from ADMIN_EMAILS")
	}
}

func TestBuiltInAdminsAreAlwaysPresent(t *testing.T) {
	admins := parseEmailSet("")
	for _, email := range []string{
		"toddzheng024@gmail.com",
		"gengcongkai456789@gmail.com",
		"zhangjiahang0725@gmail.com",
	} {
		if !admins[email] {
			t.Fatalf("missing built-in admin %s", email)
		}
	}

	admins = parseEmailSet("extra@example.com")
	if !admins["extra@example.com"] || !admins["toddzheng024@gmail.com"] {
		t.Fatalf("ADMIN_EMAILS should extend built-in admins: %#v", admins)
	}
}

func TestNewServerMergesBuiltInAdmins(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
	srv := newServer(Config{
		AdminEmails: map[string]bool{"extra@example.com": true},
	}, NewMemoryStore(now))
	srv.clock = now

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "toddzheng024@gmail.com")
	principal, err := srv.userPrincipal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.IsAdmin {
		t.Fatal("built-in admin was not granted admin principal")
	}
	if !srv.cfg.AdminEmails["extra@example.com"] {
		t.Fatal("extra admin was not preserved")
	}
}

func TestUserAPIKeyAuthenticatesWebsocketAndCanBeRevoked(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")

	createBody := []byte(`{"name":"workstation paxd"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/user/api-keys", bytes.NewReader(createBody))
	req.Header.Set("Cf-Access-Authenticated-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create api key code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created CreateUserAPIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Key == "" || created.APIKey.KeyID == "" || created.APIKey.Prefix == "" {
		t.Fatalf("bad api key response: %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/api-keys", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list api keys code = %d, body = %s", rec.Code, rec.Body.String())
	}

	user, err := srv.store.AuthenticateUserAPIKey(req.Context(), hashSecret(created.Key))
	if err != nil {
		t.Fatalf("api key did not authenticate: %v", err)
	}
	if user.Email != "todd@example.com" {
		t.Fatalf("api key owner = %s", user.Email)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/user/api-keys/"+created.APIKey.KeyID, nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke api key code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/agent/ws?key="+created.Key, nil)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked websocket auth code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestWebsocketRejectsMissingAPIKey(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	req := httptest.NewRequest(http.MethodGet, "/api/agent/ws", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func testServer(t *testing.T, ownerEmail string) (*Server, string) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
	store := NewMemoryStore(now)
	srv := newServer(Config{
		LocalUserID: "local@example.com",
		AdminEmails: map[string]bool{"admin@example.com": true},
	}, store)
	srv.clock = now

	tokenReq := httptest.NewRequest(http.MethodPost, "/api/user/agent-registration-tokens", bytes.NewReader([]byte(`{}`)))
	tokenReq.Header.Set("Cf-Access-Authenticated-User-Email", ownerEmail)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusCreated {
		t.Fatalf("registration token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	var tokenResp CreateRegistrationTokenResponse
	if err := json.Unmarshal(tokenRec.Body.Bytes(), &tokenResp); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/register", bytes.NewReader([]byte(`{
		"name":"workstation",
		"hostname":"workstation",
		"agentType":"hermes",
		"os":"linux"
	}`)))
	req.Header.Set("X-Registration-Token", tokenResp.Token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var registered RegisterAgentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	if registered.AgentID == "" || registered.APIKey == "" {
		t.Fatalf("bad register response: %+v", registered)
	}
	return srv, registered.APIKey
}

func testAgentID(t *testing.T, srv *Server, userEmail string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("Cf-Access-Authenticated-User-Email", userEmail)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agents code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("agents len = %d", len(got.Agents))
	}
	return got.Agents[0].AgentID
}

func assertPayloadField(t *testing.T, payload json.RawMessage, field string, want string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got[field] != want {
		t.Fatalf("payload[%s] = %v, want %s", field, got[field], want)
	}
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}

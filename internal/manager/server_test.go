package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/gorilla/websocket"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

func TestRequestLogIDHeaderIsPropagated(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")

	req := httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(`{}`))
	req.Header.Set(logging.HeaderRequestID, "req_test_123")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("echo code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(logging.HeaderLogID); got != "req_test_123" {
		t.Fatalf("log id header = %q, want request id", got)
	}
}

func TestRequestLogIDHeaderIsGenerated(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")

	req := httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("echo code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(logging.HeaderLogID); !strings.HasPrefix(got, "log_") {
		t.Fatalf("log id header = %q, want generated log id", got)
	}
}

func TestOpenAPIDocumentUsesRequestHost(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	req.Host = "api.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var doc struct {
		OpenAPI string `json:"openapi"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version = %q", doc.OpenAPI)
	}
	if len(doc.Servers) != 1 || doc.Servers[0].URL != "https://api.example.com" {
		t.Fatalf("servers = %+v", doc.Servers)
	}
	if _, ok := doc.Paths["/api/v1/user/{user_id}/api-keys"]; !ok {
		t.Fatalf("missing /api/v1/user/{user_id}/api-keys path")
	}
	if _, ok := doc.Paths["/api/v1/user/{user_id}/nodes/{node_id}/agents/{agent_id}/sessions/{session_id}/messages"]; !ok {
		t.Fatalf("missing /api/v1 user node agent session messages path")
	}
	if _, ok := doc.Paths["/api/v1/user/{user_id}/agents/{agent_id}/sessions/{session_id}/history"]; !ok {
		t.Fatalf("missing /api/v1 user agent session history path")
	}
	if _, ok := doc.Paths["/api/v1/user/{user_id}/nodes/{node_id}/agents/{agent_id}/sessions/{session_id}"]; !ok {
		t.Fatalf("missing /api/v1 user node agent session path")
	}
	if _, ok := doc.Paths["/api/v1/agent/tunnel"]; !ok {
		t.Fatalf("missing /api/v1/agent/tunnel websocket path")
	}
	if _, ok := doc.Paths["/api/v1/node/agents/register"]; !ok {
		t.Fatalf("missing /api/v1/node/agents/register path")
	}
	if _, ok := doc.Paths["/api/v1/user/{user_id}/agents/{agent_id}/tunnel"]; !ok {
		t.Fatalf("missing /api/v1/user/{user_id}/agents/{agent_id}/tunnel websocket path")
	}
	if _, ok := doc.Paths["/api/v1/user/{user_id}/agents/{agent_id}/sessions/{session_id}/tunnel"]; !ok {
		t.Fatalf("missing /api/v1/user session ACP tunnel websocket path")
	}
	if _, ok := doc.Paths["/api/v1/public/paxd/download"]; !ok {
		t.Fatalf("missing /api/v1/public/paxd/download path")
	}
	if _, ok := doc.Paths["/api/v1/public/paxd/install.sh"]; !ok {
		t.Fatalf("missing /api/v1/public/paxd/install.sh path")
	}
	if _, ok := doc.Paths["/api/v1/admin/paxd/artifacts"]; !ok {
		t.Fatalf("missing /api/v1/admin/paxd/artifacts path")
	}
	for _, removed := range []string{
		"/api/user/sessions/{sessionId}",
		"/api/user/sessions/{sessionId}/messages",
		"/api/user/message",
		"/api/user/mailbox",
	} {
		if _, ok := doc.Paths[removed]; ok {
			t.Fatalf("removed path still present: %s", removed)
		}
	}
}

func TestNodeRegistrationSessionConnectsNodeAfterUserApproval(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")

	startReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/registration/start",
		bytes.NewReader([]byte(`{
			"name":"workstation",
			"hostname":"workstation.local",
			"machine_type":"mac",
			"os":"darwin",
			"arch":"arm64",
			"paxd_version":"0.1.0",
			"api_endpoint":"http://localhost:8642"
		}`)),
	)
	startReq.Host = "pax.example.com"
	startReq.Header.Set("X-Forwarded-Proto", "https")
	setJSON(startReq)
	startRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start code = %d, body = %s", startRec.Code, startRec.Body.String())
	}
	start := decodeData[StartNodeRegistrationResponse](t, startRec.Body.Bytes())
	if len(start.PairCode) != 6 || start.PollToken == "" || start.RegistrationID == "" {
		t.Fatalf("bad start response: %+v", start)
	}
	if start.VerificationURI != "https://pax.example.com/connect.html" {
		t.Fatalf("verification uri = %q", start.VerificationURI)
	}

	pollBody := []byte(
		`{"registration_id":"` + start.RegistrationID + `","poll_token":"` + start.PollToken + `"}`,
	)
	pollReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/registration/poll",
		bytes.NewReader(pollBody),
	)
	setJSON(pollReq)
	pollRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(pollRec, pollReq)
	if pollRec.Code != http.StatusOK {
		t.Fatalf("pending poll code = %d, body = %s", pollRec.Code, pollRec.Body.String())
	}
	pending := decodeData[PollNodeRegistrationResponse](t, pollRec.Body.Bytes())
	if pending.Status != "pending" || pending.APIKey != "" {
		t.Fatalf("pending poll = %+v", pending)
	}

	approveReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registrations/"+start.PairCode+"/approve",
		bytes.NewReader([]byte(`{}`)),
	)
	setJSON(approveReq)
	approveReq.Header.Set("X-User-Email", "owner@example.com")
	approveRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(approveRec, approveReq)
	if approveRec.Code != http.StatusOK {
		t.Fatalf("approve code = %d, body = %s", approveRec.Code, approveRec.Body.String())
	}

	pollReq = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/registration/poll",
		bytes.NewReader(pollBody),
	)
	setJSON(pollReq)
	pollRec = httptest.NewRecorder()
	srv.routes().ServeHTTP(pollRec, pollReq)
	if pollRec.Code != http.StatusOK {
		t.Fatalf("approved poll code = %d, body = %s", pollRec.Code, pollRec.Body.String())
	}
	approved := decodeData[PollNodeRegistrationResponse](t, pollRec.Body.Bytes())
	if approved.Status != "approved" || approved.NodeID == "" || approved.APIKey == "" {
		t.Fatalf("approved poll = %+v", approved)
	}

	statusReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/status",
		bytes.NewReader([]byte(`{"hostname":"workstation.local","agents":[]}`)),
	)
	setJSON(statusReq)
	statusReq.Header.Set("X-Pax-Key", approved.APIKey)
	statusRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("node status code = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}

	nextStartReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/registration/start",
		bytes.NewReader([]byte(`{"hostname":"next.local","os":"darwin"}`)),
	)
	setJSON(nextStartReq)
	nextStartRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(nextStartRec, nextStartReq)
	if nextStartRec.Code != http.StatusOK {
		t.Fatalf("next start code = %d, body = %s", nextStartRec.Code, nextStartRec.Body.String())
	}

	pollReq = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/registration/poll",
		bytes.NewReader(pollBody),
	)
	setJSON(pollReq)
	pollRec = httptest.NewRecorder()
	srv.routes().ServeHTTP(pollRec, pollReq)
	if pollRec.Code != http.StatusUnauthorized {
		t.Fatalf("stale poll code = %d, body = %s", pollRec.Code, pollRec.Body.String())
	}
}

func TestOpenAPIUI(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")

	req := httptest.NewRequest(http.MethodGet, "/openapi", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi ui code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	body := rec.Body.String()
	for _, want := range []string{"pax-manager API", "fetch(\"openapi.json\")", "Open JSON"} {
		if !strings.Contains(body, want) {
			t.Fatalf("openapi ui missing %q", want)
		}
	}
}

func TestPaxdArtifactPublishAndDownload(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	srv.cfg.PaxdArtifactUploadAudience = "https://manager.example.com"
	srv.cfg.PaxdArtifactUploadPrincipals = map[string]bool{
		"release-bot@example.iam.gserviceaccount.com": true,
	}
	srv.cfg.PaxdArtifactDownloadTTL = time.Minute
	fakeBackend := &fakePaxdArtifactBackend{
		principal: "release-bot@example.iam.gserviceaccount.com",
		attrs: paxdArtifactObjectAttrs{
			Generation:  12345,
			SizeBytes:   4096,
			ContentType: "application/octet-stream",
		},
	}
	srv.paxdArtifacts = fakeBackend

	publishReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/paxd/artifacts",
		bytes.NewReader([]byte(`{
			"platform":"Linux/AMD64",
			"tags":["stable","latest"],
			"version":"v0.1.2",
			"build_id":"build-123",
			"bucket":"paxd-releases",
			"object":"paxd/v0.1.2/linux-amd64/paxd",
			"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		}`)),
	)
	setJSON(publishReq)
	publishReq.Header.Set("Authorization", "Bearer valid-token")
	publishRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(publishRec, publishReq)
	if publishRec.Code != http.StatusOK {
		t.Fatalf("publish code = %d, body = %s", publishRec.Code, publishRec.Body.String())
	}
	published := decodeData[struct {
		Artifact PaxdArtifact `json:"artifact"`
	}](t, publishRec.Body.Bytes())
	if published.Artifact.Platform != "linux/amd64" {
		t.Fatalf("platform = %q", published.Artifact.Platform)
	}
	if published.Artifact.Generation != 12345 || published.Artifact.SizeBytes != 4096 {
		t.Fatalf("artifact attrs = %+v", published.Artifact)
	}
	if got := strings.Join(published.Artifact.Tags, ","); got != "latest,stable" {
		t.Fatalf("tags = %q", got)
	}

	downloadReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/public/paxd/download?platform=linux/amd64&tags=stable",
		nil,
	)
	downloadRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("download code = %d, body = %s", downloadRec.Code, downloadRec.Body.String())
	}
	download := decodeData[PaxdArtifactDownloadResponse](t, downloadRec.Body.Bytes())
	if download.URL != "https://signed.example/paxd/v0.1.2/linux-amd64/paxd" {
		t.Fatalf("signed url = %q", download.URL)
	}
	if !download.ExpiresAt.Equal(srv.clock().UTC().Add(time.Minute)) {
		t.Fatalf("expires_at = %s", download.ExpiresAt)
	}
	if fakeBackend.signedArtifact.ArtifactID != published.Artifact.ArtifactID {
		t.Fatalf("signed artifact = %+v", fakeBackend.signedArtifact)
	}
}

func TestPaxdInstallerRedirect(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	srv.cfg.PaxdArtifactDownloadTTL = time.Minute
	srv.cfg.PaxdInstallerBucket = "pax-tech-bucket"
	srv.cfg.PaxdInstallerObject = "script/installer.sh"
	fakeBackend := &fakePaxdArtifactBackend{}
	srv.paxdArtifacts = fakeBackend

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/paxd/install.sh", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("installer code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "https://signed.example/script/installer.sh" {
		t.Fatalf("location = %q", got)
	}
	if fakeBackend.signedArtifact.Bucket != "pax-tech-bucket" {
		t.Fatalf("signed bucket = %q", fakeBackend.signedArtifact.Bucket)
	}
	if fakeBackend.signedArtifact.Object != "script/installer.sh" {
		t.Fatalf("signed object = %q", fakeBackend.signedArtifact.Object)
	}
	if !fakeBackend.expiresAt.Equal(srv.clock().UTC().Add(time.Minute)) {
		t.Fatalf("expires_at = %s", fakeBackend.expiresAt)
	}
}

func TestPaxdArtifactPublishRequiresBearerToken(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	srv.cfg.PaxdArtifactUploadAudience = "https://manager.example.com"
	srv.cfg.PaxdArtifactUploadPrincipals = map[string]bool{
		"release-bot@example.iam.gserviceaccount.com": true,
	}
	srv.paxdArtifacts = &fakePaxdArtifactBackend{
		principal: "release-bot@example.iam.gserviceaccount.com",
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/paxd/artifacts",
		bytes.NewReader([]byte(`{}`)),
	)
	setJSON(req)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestRequestBodyLimit(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	srv.maxBodyBytes = 8

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/echo",
		strings.NewReader(`{"message":"too large"}`),
	)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAPIRateLimit(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	srv, _ := testServer(t, "todd@example.com")
	srv.apiLimiter = newRateLimiter(60, 1, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(`{}`))
	req.RemoteAddr = "198.51.100.10:1234"
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(`{}`))
	req.RemoteAddr = "198.51.100.10:1234"
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentStatusUsesPaxdSessionShape(t *testing.T) {
	srv, apiKey := testServer(t, "todd@example.com")

	statusBody := []byte(`{
		"hostname":"workstation",
		"sessions":[{
			"session_id":"sess-1",
			"agent_type":"hermes",
			"native_id":"resp-1",
			"name":"repo task",
			"project_id":"repo",
			"preview":"fix tests",
			"workspace_roots":["/workspace/repo"],
			"status":"running",
			"current_task":"go test ./...",
			"message_count":7,
			"token_usage":{"total_tokens":123},
			"model":"gpt-test",
			"run_id":"run-1",
			"run_status":"running"
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/status", bytes.NewReader(statusBody))
	setJSON(req)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body = %s", rec.Code, rec.Body.String())
	}

	agentID := testAgentID(t, srv, "todd@example.com")
	req = httptest.NewRequest(
		http.MethodGet,
		"/api/user/agents/"+agentID,
		nil,
	)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent code = %d, body = %s", rec.Code, rec.Body.String())
	}
	agent := decodeData[Agent](t, rec.Body.Bytes())
	if agent.AgentID != agentID || agent.Hostname != "workstation" {
		t.Fatalf("unexpected agent: %+v", agent)
	}

	req = httptest.NewRequest(
		http.MethodGet,
		"/api/user/agents/"+agentID+"/sessions",
		nil,
	)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions code = %d, body = %s", rec.Code, rec.Body.String())
	}

	got := decodeData[struct {
		Sessions []AgentSession `json:"sessions"`
	}](t, rec.Body.Bytes())
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions len = %d", len(got.Sessions))
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("native_id")) {
		t.Fatalf("user session response leaked native_id: %s", rec.Body.String())
	}
	session := got.Sessions[0]
	if session.SessionID == "" || session.SessionID == "sess-1" || session.AgentType != "hermes" ||
		session.TokenTotal != 123 {
		t.Fatalf("unexpected session: %+v", session)
	}
	if len(session.WorkspaceRoots) != 1 || session.WorkspaceRoots[0] != "/workspace/repo" {
		t.Fatalf("workspace roots = %#v", session.WorkspaceRoots)
	}

	req = httptest.NewRequest(
		http.MethodGet,
		"/api/user/agents/"+agentID+"/sessions/"+session.SessionID,
		nil,
	)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session code = %d, body = %s", rec.Code, rec.Body.String())
	}
	gotSession := decodeData[AgentSession](t, rec.Body.Bytes())
	if gotSession.SessionID != session.SessionID || gotSession.AgentID != agentID {
		t.Fatalf("unexpected session detail: %+v", gotSession)
	}
}

func TestMailboxLifecycle(t *testing.T) {
	srv, apiKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")
	sessionID := reportTestSession(t, srv, apiKey, "todd@example.com", agentID, "sess-1")

	body := []byte(`{
		"message":"run the tests",
		"message_type":"chat"
	}`)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/user/agents/"+agentID+"/sessions/"+sessionID+"/messages",
		bytes.NewReader(body),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create message code = %d, body = %s", rec.Code, rec.Body.String())
	}

	created := decodeData[MailboxMessage](t, rec.Body.Bytes())
	if created.Payload == nil || !json.Valid(created.Payload) {
		t.Fatalf("payload is not valid JSON: %s", created.Payload)
	}
	assertPayloadField(t, created.Payload, "entity_type", "turn")
	assertPayloadField(t, created.Payload, "event_type", "start")
	assertPayloadField(t, created.Payload, "prompt", "run the tests")

	req = httptest.NewRequest(
		http.MethodGet,
		"/api/agent/sessions/sess-1/mailbox?offset=0&limit=10",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull code = %d, body = %s", rec.Code, rec.Body.String())
	}

	pull := decodeData[MailboxPull](t, rec.Body.Bytes())
	if len(pull.Messages) != 1 {
		t.Fatalf("pull messages len = %d", len(pull.Messages))
	}
	if pull.Messages[0].Status != "delivered" || pull.MaxOffset != pull.Messages[0].ID {
		t.Fatalf("unexpected pull: %+v", pull)
	}
	if pull.Messages[0].SessionID != "sess-1" {
		t.Fatalf("node pull did not translate session to native id: %+v", pull.Messages[0])
	}
	assertPayloadField(t, pull.Messages[0].Payload, "session_id", "sess-1")

	resultBody := []byte(`{"status":"completed","result":"tests passed"}`)
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/agent/messages/"+created.MessageID+"/result",
		bytes.NewReader(resultBody),
	)
	setJSON(req)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("result code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(
		http.MethodPost,
		"/api/agent/messages/offset",
		bytes.NewReader([]byte(`{"offset":`+int64String(pull.MaxOffset)+`}`)),
	)
	setJSON(req)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("offset code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(
		http.MethodGet,
		"/api/user/agents/"+agentID+"/sessions/"+sessionID+"/messages",
		nil,
	)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mailbox code = %d, body = %s", rec.Code, rec.Body.String())
	}

	mailbox := decodeData[struct {
		Messages []MailboxMessage `json:"messages"`
	}](t, rec.Body.Bytes())
	if len(mailbox.Messages) != 1 || mailbox.Messages[0].Result != "tests passed" {
		t.Fatalf("unexpected mailbox: %+v", mailbox.Messages)
	}
}

func TestTenantIsolationAndAdminPrincipalDoesNotBypassOwnerScope(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", "ellen@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ellen agents code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[struct {
		Agents []Agent `json:"agents"`
	}](t, rec.Body.Bytes())
	if len(got.Agents) != 0 {
		t.Fatalf("ellen saw agents: %+v", got.Agents)
	}

	body := []byte(`{"message":"cross tenant","message_type":"chat"}`)
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/user/agents/"+agentID+"/sessions/sess-1/messages",
		bytes.NewReader(body),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "ellen@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant send code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", "admin@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin agents code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got = decodeData[struct {
		Agents []Agent `json:"agents"`
	}](t, rec.Body.Bytes())
	if len(got.Agents) != 0 {
		t.Fatalf("admin agents len = %d", len(got.Agents))
	}
}

func TestUserIdentityRejectsLocalHeaderUnlessEnabled(t *testing.T) {
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

	srv = newServer(Config{
		LocalUserID:          "local@example.com",
		AllowLocalUserHeader: true,
		AdminEmails:          map[string]bool{"admin@example.com": true},
	}, NewMemoryStore(now))
	srv.clock = now
	req = httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", "admin@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local header code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminStatusFollowsCurrentConfig(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
	store := NewMemoryStore(now)
	srv := newServer(Config{
		AllowLocalUserHeader: true,
		AdminEmails:          map[string]bool{"admin@example.com": true},
	}, store)
	srv.clock = now

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", "admin@example.com")
	if _, err := userPrincipalFromHTTPRequest(t, srv, req); err != nil {
		t.Fatal(err)
	}

	srv.cfg.AdminEmails = map[string]bool{}
	principal, err := userPrincipalFromHTTPRequest(t, srv, req)
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
		AllowLocalUserHeader: true,
		AdminEmails:          map[string]bool{"extra@example.com": true},
	}, NewMemoryStore(now))
	srv.clock = now

	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", "toddzheng024@gmail.com")
	principal, err := userPrincipalFromHTTPRequest(t, srv, req)
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

func TestUserAPIKeyCanBeCreatedListedAndRevoked(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")

	createBody := []byte(`{"name":"workstation paxd"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/user/api-keys", bytes.NewReader(createBody))
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create api key code = %d, body = %s", rec.Code, rec.Body.String())
	}
	created := decodeData[CreateUserAPIKeyResponse](t, rec.Body.Bytes())
	if created.Key == "" || created.APIKey.KeyID == "" || created.APIKey.Prefix == "" {
		t.Fatalf("bad api key response: %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/user/api-keys", nil)
	req.Header.Set("X-User-Email", "todd@example.com")
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
	req.Header.Set("X-User-Email", "todd@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke api key code = %d, body = %s", rec.Code, rec.Body.String())
	}

}

func TestAgentWebsocketAuthenticatesOwnerAndProcessesMailboxFrames(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")
	sessionID := reportTestSession(t, srv, paxKey, "todd@example.com", agentID, "sess-1")

	body := []byte(`{
		"message":"run the tests",
		"message_type":"chat"
	}`)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/user/agents/"+agentID+"/sessions/"+sessionID+"/messages",
		bytes.NewReader(body),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create message code = %d, body = %s", rec.Code, rec.Body.String())
	}
	created := decodeData[MailboxMessage](t, rec.Body.Bytes())

	wsReq := httptest.NewRequest(
		http.MethodGet,
		"/api/agent/ws?agent_id="+agentID+"&session_id=sess-1",
		nil,
	)
	wsReq.Header.Set("X-Pax-Key", paxKey)
	owner, agent, initial, err := srv.authenticateAgentWS(wsReq)
	if err != nil {
		t.Fatalf("authenticate websocket: %v", err)
	}
	if owner.Email != "todd@example.com" || agent.AgentID != agentID {
		t.Fatalf("unexpected websocket identity owner=%+v agent=%+v", owner, agent)
	}
	if initial.SessionID != "sess-1" {
		t.Fatalf("initial session = %q", initial.SessionID)
	}

	pullResp := srv.handleAgentWSRequest(wsReq.Context(), agent, "sess-1", agentWSRequest{
		Type:      "pull_mailbox",
		RequestID: "pull-1",
		Data:      json.RawMessage(`{"offset":0,"limit":10}`),
	})

	if pullResp.Type != "pull_mailbox_result" || pullResp.RequestID != "pull-1" ||
		pullResp.Code != http.StatusOK {
		t.Fatalf("pull response = %+v", pullResp)
	}
	var pull MailboxPull
	pullData, _ := json.Marshal(pullResp.Data)
	if err := json.Unmarshal(pullData, &pull); err != nil {
		t.Fatal(err)
	}
	if len(pull.Messages) != 1 || pull.Messages[0].MessageID != created.MessageID {
		t.Fatalf("pulled messages = %+v", pull.Messages)
	}

	resultResp := srv.handleAgentWSRequest(wsReq.Context(), agent, "sess-1", agentWSRequest{
		Type:      "message_result",
		RequestID: "result-1",
		Data: json.RawMessage(
			`{"message_id":"` + created.MessageID + `","status":"completed","result":"tests passed"}`,
		),
	})
	if resultResp.Type != "message_result_result" || resultResp.RequestID != "result-1" ||
		resultResp.Code != http.StatusOK {
		t.Fatalf("result response = %+v", resultResp)
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

func TestACPTunnelRelaysFramesBetweenUserAndAgent(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleUserACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentHeader := http.Header{"X-Pax-Key": []string{paxKey}}
	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID,
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer func() { _ = agentWS.Close() }()

	userHeader := http.Header{"X-User-Email": []string{"todd@example.com"}}
	userWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/tunnel",
		userHeader,
	)
	if err != nil {
		t.Fatalf("dial user tunnel: %v", err)
	}
	defer func() { _ = userWS.Close() }()

	requestPayload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if err := userWS.WriteMessage(websocket.TextMessage, requestPayload); err != nil {
		t.Fatalf("write user request: %v", err)
	}
	messageType, gotRequest, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read agent request: %v", err)
	}
	requestEnv := decodeACPTunnelEnvelope(t, gotRequest)
	if messageType != websocket.TextMessage ||
		requestEnv.Type != acpTunnelTypeData ||
		requestEnv.Stream != acpTunnelStreamManagerToPaxd ||
		requestEnv.Seq != 1 ||
		string(requestEnv.Payload) != string(requestPayload) {
		t.Fatalf("agent got type=%d payload=%s", messageType, gotRequest)
	}
	waitTransportStatus(
		t,
		srv,
		agentID,
		domain.TransportStreamManagerToPaxd,
		1,
		domain.TransportDirectionOutbound,
		domain.TransportStatusSent,
	)
	requestAck := mustMarshalACPTunnelEnvelope(t, acpTunnelEnvelope{
		Type:   acpTunnelTypeAck,
		Stream: acpTunnelStreamManagerToPaxd,
		Seq:    1,
	})
	if err := agentWS.WriteMessage(websocket.TextMessage, requestAck); err != nil {
		t.Fatalf("write agent request ack: %v", err)
	}
	waitTransportStatus(
		t,
		srv,
		agentID,
		domain.TransportStreamManagerToPaxd,
		1,
		domain.TransportDirectionOutbound,
		domain.TransportStatusAcked,
	)

	responsePayload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`)
	responseEnv := mustMarshalACPTunnelEnvelope(t, acpTunnelEnvelope{
		Type:    acpTunnelTypeData,
		Stream:  acpTunnelStreamPaxdToManager,
		Seq:     1,
		Payload: json.RawMessage(responsePayload),
	})
	if err := agentWS.WriteMessage(websocket.TextMessage, responseEnv); err != nil {
		t.Fatalf("write agent response: %v", err)
	}
	messageType, gotAck, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read agent response ack: %v", err)
	}
	responseAck := decodeACPTunnelEnvelope(t, gotAck)
	if messageType != websocket.TextMessage ||
		responseAck.Type != acpTunnelTypeAck ||
		responseAck.Stream != acpTunnelStreamPaxdToManager ||
		responseAck.Seq != 1 {
		t.Fatalf("agent got ack type=%d payload=%s", messageType, gotAck)
	}
	messageType, gotResponse, err := userWS.ReadMessage()
	if err != nil {
		t.Fatalf("read user response: %v", err)
	}
	if messageType != websocket.TextMessage || string(gotResponse) != string(responsePayload) {
		t.Fatalf("user got type=%d payload=%s", messageType, gotResponse)
	}
	waitTransportStatus(
		t,
		srv,
		agentID,
		domain.TransportStreamPaxdToManager,
		1,
		domain.TransportDirectionInbound,
		domain.TransportStatusApplied,
	)
}

func TestACPTunnelRecordedTrafficProjectsAggregatedHistory(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleUserACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentHeader := http.Header{"X-Pax-Key": []string{paxKey}}
	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID,
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer agentWS.Close()

	userHeader := http.Header{"X-User-Email": []string{"todd@example.com"}}
	userWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/tunnel",
		userHeader,
	)
	if err != nil {
		t.Fatalf("dial user tunnel: %v", err)
	}
	defer userWS.Close()

	initialize := []byte(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
	)
	if err := userWS.WriteMessage(websocket.TextMessage, initialize); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	_, gotInitialize, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read agent initialize: %v", err)
	}
	initializeEnv := decodeACPTunnelEnvelope(t, gotInitialize)
	if initializeEnv.Type != acpTunnelTypeData ||
		initializeEnv.Stream != acpTunnelStreamManagerToPaxd ||
		initializeEnv.Seq != 1 ||
		string(initializeEnv.Payload) != string(initialize) {
		t.Fatalf("initialize envelope = %s", gotInitialize)
	}
	if err := agentWS.WriteMessage(websocket.TextMessage, mustMarshalACPTunnelEnvelope(t, acpTunnelEnvelope{
		Type:   acpTunnelTypeAck,
		Stream: acpTunnelStreamManagerToPaxd,
		Seq:    initializeEnv.Seq,
	})); err != nil {
		t.Fatalf("write initialize ack: %v", err)
	}

	initializeResponse := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`)
	writeAgentDataFrame(t, agentWS, 1, initializeResponse)
	readAgentAck(t, agentWS, acpTunnelStreamPaxdToManager, 1)
	_, gotInitializeResponse, err := userWS.ReadMessage()
	if err != nil {
		t.Fatalf("read user initialize response: %v", err)
	}
	if string(gotInitializeResponse) != string(initializeResponse) {
		t.Fatalf("initialize response = %s", gotInitializeResponse)
	}

	firstDelta := json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"h"}}}}`,
	)
	secondDelta := json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"i"}}}}`,
	)
	thoughtDelta := json.RawMessage(
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}}}`,
	)
	writeAgentDataFrame(t, agentWS, 2, firstDelta)
	readAgentAck(t, agentWS, acpTunnelStreamPaxdToManager, 2)
	_, gotFirstDelta, err := userWS.ReadMessage()
	if err != nil {
		t.Fatalf("read first user delta: %v", err)
	}
	if string(gotFirstDelta) != string(firstDelta) {
		t.Fatalf("first user delta = %s", gotFirstDelta)
	}
	writeAgentDataFrame(t, agentWS, 3, secondDelta)
	readAgentAck(t, agentWS, acpTunnelStreamPaxdToManager, 3)
	_, gotSecondDelta, err := userWS.ReadMessage()
	if err != nil {
		t.Fatalf("read second user delta: %v", err)
	}
	if string(gotSecondDelta) != string(secondDelta) {
		t.Fatalf("second user delta = %s", gotSecondDelta)
	}
	writeAgentDataFrame(t, agentWS, 4, thoughtDelta)
	readAgentAck(t, agentWS, acpTunnelStreamPaxdToManager, 4)
	_, gotThoughtDelta, err := userWS.ReadMessage()
	if err != nil {
		t.Fatalf("read thought user delta: %v", err)
	}
	if string(gotThoughtDelta) != string(thoughtDelta) {
		t.Fatalf("thought user delta = %s", gotThoughtDelta)
	}

	messages, err := srv.store.ListMessages(t.Context(), agentID, "sess-1", 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %+v, want message and thought aggregates", messages)
	}
	gotPartsByType := make(map[string]string)
	for _, msg := range messages {
		if msg.Source != domain.MessageSourceACPTunnel ||
			msg.Direction != domain.MessageDirectionAgentToUser ||
			msg.Role != "assistant" ||
			msg.OwnerUserID == "" ||
			msg.NodeID == "" ||
			len(msg.RawJSON) != 0 ||
			!strings.HasPrefix(msg.MessageID, "msg_") ||
			!strings.HasPrefix(msg.LogicalKey, "acp:") ||
			strings.Contains(msg.MessageID, "rpc:") {
			t.Fatalf("projected message = %+v", msg)
		}
		parts, err := srv.store.ListMessageParts(t.Context(), msg.MessageID)
		if err != nil {
			t.Fatalf("list message parts: %v", err)
		}
		if len(parts) != 1 ||
			parts[0].PartIndex != 0 ||
			parts[0].PartType != domain.MessagePartText ||
			len(parts[0].PayloadJSON) != 0 {
			t.Fatalf("parts for %s = %+v, want one text part", msg.MessageID, parts)
		}
		gotPartsByType[msg.MessageType] = parts[0].Text
	}
	if gotPartsByType["agent_message_chunk"] != "hi" {
		t.Fatalf("agent_message_chunk text = %q, want hi", gotPartsByType["agent_message_chunk"])
	}
	if gotPartsByType["agent_thought_chunk"] != "thinking" {
		t.Fatalf(
			"agent_thought_chunk text = %q, want thinking",
			gotPartsByType["agent_thought_chunk"],
		)
	}
	allMessages, err := srv.store.ListMessages(t.Context(), agentID, "", 100)
	if err != nil {
		t.Fatalf("list all messages: %v", err)
	}
	for _, candidate := range allMessages {
		if strings.Contains(candidate.MessageID, "rpc:") {
			t.Fatalf("rpc-derived message should not be projected: %+v", candidate)
		}
	}
}

func TestACPTunnelReplaysUnackedUserFrameAfterAgentReconnect(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleUserACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentHeader := http.Header{"X-Pax-Key": []string{paxKey}}
	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID,
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial first agent tunnel: %v", err)
	}

	userHeader := http.Header{"X-User-Email": []string{"todd@example.com"}}
	userWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/tunnel",
		userHeader,
	)
	if err != nil {
		t.Fatalf("dial user tunnel: %v", err)
	}
	defer userWS.Close()

	requestPayload := []byte(
		`{"jsonrpc":"2.0","id":7,"method":"session/new","params":{"cwd":"/tmp"}}`,
	)
	if err := userWS.WriteMessage(websocket.TextMessage, requestPayload); err != nil {
		t.Fatalf("write user request: %v", err)
	}
	_, gotRequest, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read first agent request: %v", err)
	}
	firstEnv := decodeACPTunnelEnvelope(t, gotRequest)
	if firstEnv.Stream != acpTunnelStreamManagerToPaxd ||
		firstEnv.Seq != 1 ||
		string(firstEnv.Payload) != string(requestPayload) {
		t.Fatalf("first agent payload = %s", gotRequest)
	}
	if err := agentWS.Close(); err != nil {
		t.Fatalf("close first agent tunnel: %v", err)
	}

	var secondAgentWS *websocket.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		secondAgentWS, _, err = websocket.DefaultDialer.Dial(
			baseWS+"/api/v1/agent/tunnel?agent_id="+agentID,
			agentHeader,
		)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial second agent tunnel: %v", err)
	}
	defer secondAgentWS.Close()

	_, replayed, err := secondAgentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read replayed request: %v", err)
	}
	replayEnv := decodeACPTunnelEnvelope(t, replayed)
	if replayEnv.Stream != acpTunnelStreamManagerToPaxd ||
		replayEnv.Seq != 1 ||
		string(replayEnv.Payload) != string(requestPayload) {
		t.Fatalf("replayed agent payload = %s", replayed)
	}
	replayAck := mustMarshalACPTunnelEnvelope(t, acpTunnelEnvelope{
		Type:   acpTunnelTypeAck,
		Stream: acpTunnelStreamManagerToPaxd,
		Seq:    replayEnv.Seq,
	})
	if err := secondAgentWS.WriteMessage(websocket.TextMessage, replayAck); err != nil {
		t.Fatalf("write replay ack: %v", err)
	}
	waitTransportStatus(
		t,
		srv,
		agentID,
		domain.TransportStreamManagerToPaxd,
		replayEnv.Seq,
		domain.TransportDirectionOutbound,
		domain.TransportStatusAcked,
	)
}

func TestACPTunnelRoutesSameAgentBySession(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleUserACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentHeader := http.Header{"X-Pax-Key": []string{paxKey}}
	agentWSA, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID+"&session_id=sess-a",
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial agent tunnel a: %v", err)
	}
	defer func() { _ = agentWSA.Close() }()
	agentWSB, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID+"&session_id=sess-b",
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial agent tunnel b: %v", err)
	}
	defer func() { _ = agentWSB.Close() }()

	userHeader := http.Header{"X-User-Email": []string{"todd@example.com"}}
	userWSA, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/sessions/sess-a/tunnel",
		userHeader,
	)
	if err != nil {
		t.Fatalf("dial user tunnel a: %v", err)
	}
	defer func() { _ = userWSA.Close() }()
	userWSB, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/sessions/sess-b/tunnel",
		userHeader,
	)
	if err != nil {
		t.Fatalf("dial user tunnel b: %v", err)
	}
	defer func() { _ = userWSB.Close() }()

	requestA := []byte(`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{}}`)
	if err := userWSA.WriteMessage(websocket.TextMessage, requestA); err != nil {
		t.Fatalf("write user request a: %v", err)
	}
	if err := agentWSA.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set agent a read deadline: %v", err)
	}
	messageType, gotRequestA, err := agentWSA.ReadMessage()
	if err != nil {
		t.Fatalf("read agent request a: %v", err)
	}
	requestEnvA := decodeACPTunnelEnvelope(t, gotRequestA)
	if messageType != websocket.TextMessage ||
		requestEnvA.Type != acpTunnelTypeData ||
		requestEnvA.Stream != acpTunnelStreamManagerToPaxd ||
		string(requestEnvA.Payload) != string(requestA) {
		t.Fatalf("agent a got type=%d payload=%s", messageType, gotRequestA)
	}

	requestB := []byte(`{"jsonrpc":"2.0","id":"b","method":"initialize","params":{}}`)
	if err := userWSB.WriteMessage(websocket.TextMessage, requestB); err != nil {
		t.Fatalf("write user request b: %v", err)
	}
	if err := agentWSB.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set agent b read deadline: %v", err)
	}
	messageType, gotRequestB, err := agentWSB.ReadMessage()
	if err != nil {
		t.Fatalf("read agent request b: %v", err)
	}
	requestEnvB := decodeACPTunnelEnvelope(t, gotRequestB)
	if messageType != websocket.TextMessage ||
		requestEnvB.Type != acpTunnelTypeData ||
		requestEnvB.Stream != acpTunnelStreamManagerToPaxd ||
		string(requestEnvB.Payload) != string(requestB) {
		t.Fatalf("agent b got type=%d payload=%s", messageType, gotRequestB)
	}
}

func TestACPTunnelRequestPermissionAddsAllowAlwaysOption(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleUserACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID+"&session_id=sess-approval",
		http.Header{"X-Pax-Key": []string{paxKey}},
	)
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer func() { _ = agentWS.Close() }()

	userWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/tunnel?session_id=sess-approval",
		http.Header{"X-User-Email": []string{"todd@example.com"}},
	)
	if err != nil {
		t.Fatalf("dial user tunnel: %v", err)
	}
	defer func() { _ = userWS.Close() }()

	requestPayload := []byte(`{
		"jsonrpc":"2.0",
		"id":11,
		"method":"session/request_permission",
		"params":{
			"options":[
				{
					"kind":"allow_always",
					"name":"Always Allow Bash(curl -s https://api.example.com/v1/status)",
					"optionId":"allow_always"
				},
				{"kind":"allow_once","name":"Allow","optionId":"allow"},
				{"kind":"reject_once","name":"Reject","optionId":"reject"}
			],
			"sessionId":"b6a71307-2480-491d-adad-0fa5aa723ae4",
			"toolCall":{
				"toolCallId":"toolu_01first",
				"rawInput":{
					"command":"curl -s https://api.example.com/v1/status",
					"description":"Fetch status from example API endpoint"
				},
				"title":"curl -s https://api.example.com/v1/status",
				"kind":"execute"
			}
		}
	}`)
	writeAgentDataFrame(t, agentWS, 1, requestPayload)
	if err := userWS.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set user read deadline: %v", err)
	}
	messageType, gotRequest, err := userWS.ReadMessage()
	if err != nil {
		t.Fatalf("read user permission request: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("message type = %d", messageType)
	}

	var frame struct {
		Method string `json:"method"`
		Params struct {
			Options []map[string]any `json:"options"`
		} `json:"params"`
	}
	if err := json.Unmarshal(gotRequest, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Method != "session/request_permission" {
		t.Fatalf("method = %q", frame.Method)
	}
	if !containsACPOption(frame.Params.Options, "allow_always_on_all_agents") {
		t.Fatalf("allow always option missing from frame: %s", gotRequest)
	}
}

func TestACPTunnelRequestPermissionUsesReusableApprovalGrant(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")
	approvalID := createTestApproval(
		t,
		srv,
		paxKey,
		agentID,
		"acp:tool_call:execute:curl -s https://api.example.com/v1/status",
	)
	decideTestApproval(t, srv, approvalID, "allow_always_on_all_agents")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID+"&session_id=sess-approval",
		http.Header{"X-Pax-Key": []string{paxKey}},
	)
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer func() { _ = agentWS.Close() }()

	requestPayload := []byte(`{
		"jsonrpc":"2.0",
		"id":12,
		"method":"session/request_permission",
		"params":{
			"options":[
				{
					"kind":"allow_always",
					"name":"Always Allow Bash(curl -s https://api.example.com/v1/status)",
					"optionId":"allow_always"
				},
				{"kind":"allow_once","name":"Allow","optionId":"allow"},
				{"kind":"reject_once","name":"Reject","optionId":"reject"}
			],
			"sessionId":"b6a71307-2480-491d-adad-0fa5aa723ae4",
			"toolCall":{
				"toolCallId":"toolu_01second",
				"rawInput":{
					"command":"curl -s https://api.example.com/v1/status",
					"description":"Fetch status from example API endpoint"
				},
				"title":"curl -s https://api.example.com/v1/status",
				"kind":"execute"
			}
		}
	}`)
	writeAgentDataFrame(t, agentWS, 1, requestPayload)
	if err := agentWS.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set agent read deadline: %v", err)
	}
	readAgentAck(t, agentWS, acpTunnelStreamPaxdToManager, 1)
	messageType, gotResponseFrame, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read agent permission response: %v", err)
	}
	gotResponse := decodeACPTunnelEnvelope(t, gotResponseFrame)
	if messageType != websocket.TextMessage ||
		gotResponse.Type != acpTunnelTypeData ||
		gotResponse.Stream != acpTunnelStreamManagerToPaxd {
		t.Fatalf("response envelope type=%d payload=%s", messageType, gotResponseFrame)
	}

	var frame struct {
		ID     int            `json:"id"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(gotResponse.Payload, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.ID != 12 {
		t.Fatalf("response id = %d", frame.ID)
	}
	if got := acpOptionKind(frame.Result); got != "allow_once" {
		t.Fatalf("selected option kind = %q, frame = %s", got, gotResponse.Payload)
	}
}

func TestACPTunnelKeepsAgentConnectedAfterUserDisconnect(t *testing.T) {
	srv, paxKey := testServer(t, "todd@example.com")
	agentID := testAgentID(t, srv, "todd@example.com")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.HandleFunc("/api/v1/user/", srv.handleUserACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentHeader := http.Header{"X-Pax-Key": []string{paxKey}}
	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentID,
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer func() { _ = agentWS.Close() }()

	userHeader := http.Header{"X-User-Email": []string{"todd@example.com"}}
	userWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/user/self/agents/"+agentID+"/tunnel",
		userHeader,
	)
	if err != nil {
		t.Fatalf("dial first user tunnel: %v", err)
	}

	firstPayload := []byte(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
	)
	if err := userWS.WriteMessage(websocket.TextMessage, firstPayload); err != nil {
		t.Fatalf("write first user request: %v", err)
	}
	_, gotRequest, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read first agent request: %v", err)
	}
	firstEnv := decodeACPTunnelEnvelope(t, gotRequest)
	if firstEnv.Stream != acpTunnelStreamManagerToPaxd ||
		firstEnv.Seq != 1 ||
		string(firstEnv.Payload) != string(firstPayload) {
		t.Fatalf("first agent payload = %s", gotRequest)
	}
	if err := userWS.Close(); err != nil {
		t.Fatalf("close first user tunnel: %v", err)
	}

	var secondUserWS *websocket.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		secondUserWS, _, err = websocket.DefaultDialer.Dial(
			baseWS+"/api/v1/user/self/agents/"+agentID+"/tunnel",
			userHeader,
		)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial second user tunnel: %v", err)
	}
	defer func() { _ = secondUserWS.Close() }()

	secondPayload := []byte(
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`,
	)
	if err := secondUserWS.WriteMessage(websocket.TextMessage, secondPayload); err != nil {
		t.Fatalf("write second user request: %v", err)
	}
	_, gotRequest, err = agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read second agent request: %v", err)
	}
	secondEnv := decodeACPTunnelEnvelope(t, gotRequest)
	if secondEnv.Stream != acpTunnelStreamManagerToPaxd ||
		secondEnv.Seq != 2 ||
		string(secondEnv.Payload) != string(secondPayload) {
		t.Fatalf("second agent payload = %s", gotRequest)
	}
}

func decodeACPTunnelEnvelope(t *testing.T, data []byte) acpTunnelEnvelope {
	t.Helper()
	var env acpTunnelEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode acp tunnel envelope %s: %v", data, err)
	}
	return env
}

func writeAgentDataFrame(
	t *testing.T,
	agentWS *websocket.Conn,
	seq int64,
	payload json.RawMessage,
) {
	t.Helper()
	frame := mustMarshalACPTunnelEnvelope(t, acpTunnelEnvelope{
		Type:    acpTunnelTypeData,
		Stream:  acpTunnelStreamPaxdToManager,
		Seq:     seq,
		Payload: payload,
	})
	if err := agentWS.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write agent data frame seq=%d: %v", seq, err)
	}
}

func readAgentAck(t *testing.T, agentWS *websocket.Conn, stream string, seq int64) {
	t.Helper()
	messageType, payload, err := agentWS.ReadMessage()
	if err != nil {
		t.Fatalf("read agent ack seq=%d: %v", seq, err)
	}
	ack := decodeACPTunnelEnvelope(t, payload)
	if messageType != websocket.TextMessage ||
		ack.Type != acpTunnelTypeAck ||
		ack.Stream != stream ||
		ack.Seq != seq {
		t.Fatalf(
			"agent ack type=%d payload=%s, want stream=%s seq=%d",
			messageType,
			payload,
			stream,
			seq,
		)
	}
}

func mustMarshalACPTunnelEnvelope(t *testing.T, env acpTunnelEnvelope) []byte {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal acp tunnel envelope: %v", err)
	}
	return data
}

func waitTransportStatus(
	t *testing.T,
	srv *Server,
	agentID string,
	stream string,
	seq int64,
	direction string,
	status string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var got string
	for time.Now().Before(deadline) {
		frame, err := srv.store.GetTransportFrame(
			t.Context(),
			agentID,
			stream,
			seq,
			direction,
		)
		if err != nil {
			t.Fatalf("get transport frame: %v", err)
		}
		if frame != nil {
			got = frame.Status
			if got == status {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transport status = %q, want %q", got, status)
}

func TestACPTunnelAcceptsNodeKeyForNodeAgent(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	userHeaders := func(req *http.Request) {
		req.Header.Set("X-User-Email", "todd@example.com")
		setJSON(req)
	}

	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	userHeaders(tokenReq)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/register",
		bytes.NewReader(
			[]byte(`{"name":"node-a","hostname":"node-a","os":"linux","arch":"arm64"}`),
		),
	)
	setJSON(registerReq)
	registerReq.Header.Set("X-Registration-Token", tokenResp.Token)
	registerRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("node register code = %d, body = %s", registerRec.Code, registerRec.Body.String())
	}
	registeredNode := decodeData[RegisterNodeResponse](t, registerRec.Body.Bytes())

	createAgentReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+registeredNode.NodeID+"/agents",
		bytes.NewReader([]byte(`{"name":"hermes-a","agent_type":"hermes"}`)),
	)
	userHeaders(createAgentReq)
	createAgentRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createAgentRec, createAgentReq)
	if createAgentRec.Code != http.StatusOK {
		t.Fatalf(
			"create node agent code = %d, body = %s",
			createAgentRec.Code,
			createAgentRec.Body.String(),
		)
	}
	agentResp := decodeData[struct {
		Agent Agent `json:"agent"`
	}](t, createAgentRec.Body.Bytes())

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	baseWS := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	agentHeader := http.Header{"X-Pax-Key": []string{registeredNode.APIKey}}
	agentWS, _, err := websocket.DefaultDialer.Dial(
		baseWS+"/api/v1/agent/tunnel?agent_id="+agentResp.Agent.AgentID,
		agentHeader,
	)
	if err != nil {
		t.Fatalf("dial agent tunnel with node key: %v", err)
	}
	defer func() { _ = agentWS.Close() }()
}

func TestRegisterNodeAgentWithRegistrationTokenCreatesNodeAndAgent(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	tokenReq.Header.Set("X-User-Email", "todd@example.com")
	setJSON(tokenReq)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/agents/register",
		bytes.NewReader([]byte(`{
			"node":{"name":"node-a","hostname":"node-a","machine_type":"linux_box","os":"linux","arch":"arm64"},
			"agent":{"name":"codex-main","agent_type":"codex"}
		}`)),
	)
	setJSON(req)
	req.Header.Set("X-Registration-Token", tokenResp.Token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register node agent code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[RegisterNodeAgentResponse](t, rec.Body.Bytes())
	if got.NodeID == "" || got.APIKey == "" || got.AgentID == "" {
		t.Fatalf("bad register node agent response: %+v", got)
	}
	if got.Agent.NodeID != got.NodeID || got.Agent.AgentID != got.AgentID {
		t.Fatalf("agent not attached to node: %+v", got)
	}
}

func TestRegisterNodeAgentWithNodeKeyAddsAgentWithoutReturningKey(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	userHeaders := func(req *http.Request) {
		req.Header.Set("X-User-Email", "todd@example.com")
		setJSON(req)
	}

	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	userHeaders(tokenReq)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/register",
		bytes.NewReader(
			[]byte(`{"name":"node-b","hostname":"node-b","os":"linux","arch":"amd64"}`),
		),
	)
	setJSON(registerReq)
	registerReq.Header.Set("X-Registration-Token", tokenResp.Token)
	registerRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("node register code = %d, body = %s", registerRec.Code, registerRec.Body.String())
	}
	registeredNode := decodeData[RegisterNodeResponse](t, registerRec.Body.Bytes())

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/agents/register",
		bytes.NewReader([]byte(`{"agent":{"name":"claude-main","agent_type":"claude-code"}}`)),
	)
	setJSON(req)
	req.Header.Set("X-Pax-Key", registeredNode.APIKey)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register node agent code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[RegisterNodeAgentResponse](t, rec.Body.Bytes())
	if got.NodeID != registeredNode.NodeID || got.AgentID == "" {
		t.Fatalf("bad register node agent response: %+v", got)
	}
	if got.APIKey != "" {
		t.Fatalf("node key should not be echoed for X-Pax-Key auth: %+v", got)
	}
	if got.Agent.NodeID != registeredNode.NodeID || got.Agent.AgentType != "claude-code" {
		t.Fatalf("agent not attached to node: %+v", got)
	}
}

func TestNodeStatusReportsAccumulateSessionBatches(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	userHeaders := func(req *http.Request) {
		req.Header.Set("X-User-Email", "todd@example.com")
		setJSON(req)
	}

	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	userHeaders(tokenReq)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/agents/register",
		bytes.NewReader([]byte(`{
			"node":{"name":"node-batch","hostname":"node-batch","os":"linux","arch":"arm64"},
			"agent":{"name":"codex-main","agent_type":"codex"}
		}`)),
	)
	setJSON(registerReq)
	registerReq.Header.Set("X-Registration-Token", tokenResp.Token)
	registerRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf(
			"register node agent code = %d, body = %s",
			registerRec.Code,
			registerRec.Body.String(),
		)
	}
	registered := decodeData[RegisterNodeAgentResponse](t, registerRec.Body.Bytes())

	for _, sessionID := range []string{"sess-batch-1", "sess-batch-2"} {
		statusReq := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/node/status",
			bytes.NewReader([]byte(`{
				"agents":[{
					"agent_id":"`+registered.AgentID+`",
					"name":"codex-main",
					"agent_type":"codex",
					"online":true,
					"sessions":[{"session_id":"`+sessionID+`","agent_type":"codex","status":"idle"}]
				}]
			}`)),
		)
		setJSON(statusReq)
		statusReq.Header.Set("X-Pax-Key", registered.APIKey)
		statusRec := httptest.NewRecorder()
		srv.routes().ServeHTTP(statusRec, statusReq)
		if statusRec.Code != http.StatusOK {
			t.Fatalf("status code = %d, body = %s", statusRec.Code, statusRec.Body.String())
		}
	}

	listReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/nodes/"+registered.NodeID+"/agents/"+registered.AgentID+"/sessions",
		nil,
	)
	userHeaders(listReq)
	listRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list sessions code = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	got := decodeData[struct {
		Sessions []AgentSession `json:"sessions"`
	}](t, listRec.Body.Bytes())
	if len(got.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want 2 accumulated sessions", got.Sessions)
	}
}

func TestNodeAPIUserNodeAgentSessionMessageRoundTrip(t *testing.T) {
	t.Run(
		"Given a Cloudflare user and a registered node when messaging a session then the node can pull, acknowledge, complete, and publish an outbound response",
		func(t *testing.T) {
			srv, _ := testServer(t, "todd@example.com")
			userHeaders := func(req *http.Request) {
				req.Header.Set("X-User-Email", "todd@example.com")
				setJSON(req)
			}

			meReq := httptest.NewRequest(http.MethodGet, "/api/v1/user/self/me", nil)
			userHeaders(meReq)
			meRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(meRec, meReq)
			if meRec.Code != http.StatusOK {
				t.Fatalf("me code = %d, body = %s", meRec.Code, meRec.Body.String())
			}

			tokenReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/node-registration-tokens",
				bytes.NewReader([]byte(`{}`)),
			)
			userHeaders(tokenReq)
			tokenRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(tokenRec, tokenReq)
			if tokenRec.Code != http.StatusOK {
				t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
			}
			tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

			registerReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/node/register",
				bytes.NewReader(
					[]byte(`{"name":"node-a","hostname":"node-a","os":"linux","arch":"arm64"}`),
				),
			)
			setJSON(registerReq)
			registerReq.Header.Set("X-Registration-Token", tokenResp.Token)
			registerRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(registerRec, registerReq)
			if registerRec.Code != http.StatusOK {
				t.Fatalf(
					"node register code = %d, body = %s",
					registerRec.Code,
					registerRec.Body.String(),
				)
			}
			registeredNode := decodeData[RegisterNodeResponse](t, registerRec.Body.Bytes())
			if registeredNode.NodeID == "" || registeredNode.APIKey == "" {
				t.Fatalf("bad node register response: %+v", registeredNode)
			}

			createAgentReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/nodes/"+registeredNode.NodeID+"/agents",
				bytes.NewReader([]byte(`{"name":"hermes-a","agent_type":"hermes"}`)),
			)
			userHeaders(createAgentReq)
			createAgentRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(createAgentRec, createAgentReq)
			if createAgentRec.Code != http.StatusOK {
				t.Fatalf(
					"create node agent code = %d, body = %s",
					createAgentRec.Code,
					createAgentRec.Body.String(),
				)
			}
			agentResp := decodeData[struct {
				Agent Agent `json:"agent"`
			}](t, createAgentRec.Body.Bytes())
			if agentResp.Agent.AgentID == "" {
				t.Fatalf("missing agent: %+v", agentResp)
			}

			createSessionReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/nodes/"+registeredNode.NodeID+"/agents/"+agentResp.Agent.AgentID+"/sessions",
				bytes.NewReader([]byte(`{"session_id":"sess-node-1","name":"first session"}`)),
			)
			userHeaders(createSessionReq)
			createSessionRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(createSessionRec, createSessionReq)
			if createSessionRec.Code != http.StatusOK {
				t.Fatalf(
					"create node session code = %d, body = %s",
					createSessionRec.Code,
					createSessionRec.Body.String(),
				)
			}

			messageReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/nodes/"+registeredNode.NodeID+"/agents/"+agentResp.Agent.AgentID+
					"/sessions/sess-node-1/messages",
				bytes.NewReader([]byte(`{"message":"build the app","message_type":"chat"}`)),
			)
			userHeaders(messageReq)
			messageRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(messageRec, messageReq)
			if messageRec.Code != http.StatusOK {
				t.Fatalf(
					"create node session message code = %d, body = %s",
					messageRec.Code,
					messageRec.Body.String(),
				)
			}
			created := decodeData[MailboxMessage](t, messageRec.Body.Bytes())

			pullReq := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/node/agents/"+agentResp.Agent.AgentID+"/sessions/sess-node-1/mailbox?offset=0&limit=10",
				nil,
			)
			pullReq.Header.Set("X-Pax-Key", registeredNode.APIKey)
			pullRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(pullRec, pullReq)
			if pullRec.Code != http.StatusOK {
				t.Fatalf(
					"pull node session mailbox code = %d, body = %s",
					pullRec.Code,
					pullRec.Body.String(),
				)
			}
			pull := decodeData[MailboxPull](t, pullRec.Body.Bytes())
			if len(pull.Messages) != 1 || pull.Messages[0].MessageID != created.MessageID {
				t.Fatalf("pulled node messages = %+v", pull.Messages)
			}

			deliveredReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/node/messages/"+created.MessageID+"/delivered",
				nil,
			)
			deliveredReq.Header.Set("X-Pax-Key", registeredNode.APIKey)
			deliveredRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(deliveredRec, deliveredReq)
			if deliveredRec.Code != http.StatusOK {
				t.Fatalf(
					"delivered code = %d, body = %s",
					deliveredRec.Code,
					deliveredRec.Body.String(),
				)
			}

			resultReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/node/messages/"+created.MessageID+"/result",
				bytes.NewReader(
					[]byte(
						`{"status":"completed","content":"done","token_usage":{"inputTokens":10,"outputTokens":5,"reasoningTokens":2}}`,
					),
				),
			)
			setJSON(resultReq)
			resultReq.Header.Set("X-Pax-Key", registeredNode.APIKey)
			resultRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(resultRec, resultReq)
			if resultRec.Code != http.StatusOK {
				t.Fatalf("result code = %d, body = %s", resultRec.Code, resultRec.Body.String())
			}

			outboundReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/node/messages/outbound",
				bytes.NewReader(
					[]byte(
						`{"agent_id":"`+agentResp.Agent.AgentID+`","session_id":"sess-node-1","content":"done","parent_message_id":"`+created.MessageID+`","token_usage":{"total_tokens":17}}`,
					),
				),
			)
			setJSON(outboundReq)
			outboundReq.Header.Set("X-Pax-Key", registeredNode.APIKey)
			outboundRec := httptest.NewRecorder()
			srv.routes().ServeHTTP(outboundRec, outboundReq)
			if outboundRec.Code != http.StatusOK {
				t.Fatalf(
					"outbound code = %d, body = %s",
					outboundRec.Code,
					outboundRec.Body.String(),
				)
			}
		},
	)
}

func TestNodeAPIUserNodeAgentSessionHistory(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	userHeaders := func(req *http.Request) {
		req.Header.Set("X-User-Email", "todd@example.com")
		setJSON(req)
	}

	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/node-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	userHeaders(tokenReq)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("node token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	registerReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/register",
		bytes.NewReader([]byte(`{"name":"node-a","hostname":"node-a","os":"linux"}`)),
	)
	setJSON(registerReq)
	registerReq.Header.Set("X-Registration-Token", tokenResp.Token)
	registerRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("node register code = %d, body = %s", registerRec.Code, registerRec.Body.String())
	}
	registeredNode := decodeData[RegisterNodeResponse](t, registerRec.Body.Bytes())

	createAgentReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+registeredNode.NodeID+"/agents",
		bytes.NewReader([]byte(`{"name":"hermes-a","agent_type":"hermes"}`)),
	)
	userHeaders(createAgentReq)
	createAgentRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createAgentRec, createAgentReq)
	if createAgentRec.Code != http.StatusOK {
		t.Fatalf(
			"create node agent code = %d, body = %s",
			createAgentRec.Code,
			createAgentRec.Body.String(),
		)
	}
	agentResp := decodeData[struct {
		Agent Agent `json:"agent"`
	}](t, createAgentRec.Body.Bytes())

	createSessionReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/nodes/"+registeredNode.NodeID+"/agents/"+agentResp.Agent.AgentID+"/sessions",
		bytes.NewReader(
			[]byte(
				`{"session_id":"sess_manager_1","native_id":"harness-session-1","name":"first session"}`,
			),
		),
	)
	userHeaders(createSessionReq)
	createSessionRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createSessionRec, createSessionReq)
	if createSessionRec.Code != http.StatusOK {
		t.Fatalf(
			"create node session code = %d, body = %s",
			createSessionRec.Code,
			createSessionRec.Body.String(),
		)
	}

	historyMessage := domain.Message{
		MessageID:   "msg_history_1",
		OwnerUserID: agentResp.Agent.OwnerUserID,
		NodeID:      registeredNode.NodeID,
		AgentID:     agentResp.Agent.AgentID,
		SessionID:   "sess_manager_1",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		Status:      "received",
		MessageType: "agent_message_chunk",
	}
	if err := srv.store.UpsertMessage(t.Context(), &historyMessage); err != nil {
		t.Fatalf("upsert history message: %v", err)
	}
	if err := srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: "msg_history_1",
		PartIndex: 0,
		PartType:  domain.MessagePartText,
		Text:      "hello from history",
	}); err != nil {
		t.Fatalf("upsert history part: %v", err)
	}
	nativeHistoryMessage := domain.Message{
		MessageID:   "msg_history_native",
		OwnerUserID: agentResp.Agent.OwnerUserID,
		NodeID:      registeredNode.NodeID,
		AgentID:     agentResp.Agent.AgentID,
		SessionID:   "harness-session-1",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		Role:        "assistant",
		Status:      "received",
		MessageType: "agent_message_chunk",
	}
	if err := srv.store.UpsertMessage(t.Context(), &nativeHistoryMessage); err != nil {
		t.Fatalf("upsert native history message: %v", err)
	}
	if err := srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: "msg_history_native",
		PartIndex: 0,
		PartType:  domain.MessagePartText,
		Text:      "hello from native history",
	}); err != nil {
		t.Fatalf("upsert native history part: %v", err)
	}
	otherSessionMessage := domain.Message{
		MessageID: "msg_history_other",
		AgentID:   agentResp.Agent.AgentID,
		SessionID: "sess-other",
		Source:    domain.MessageSourceACPTunnel,
		Direction: domain.MessageDirectionAgentToUser,
	}
	if err := srv.store.UpsertMessage(t.Context(), &otherSessionMessage); err != nil {
		t.Fatalf("upsert other history message: %v", err)
	}

	historyReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/agents/"+agentResp.Agent.AgentID+"/sessions/sess_manager_1/history",
		nil,
	)
	historyReq.Header.Set("X-User-Email", "todd@example.com")
	historyRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(historyRec, historyReq)
	if historyRec.Code != http.StatusOK {
		t.Fatalf("history code = %d, body = %s", historyRec.Code, historyRec.Body.String())
	}
	got := decodeData[struct {
		Messages []MessageWithParts `json:"messages"`
	}](t, historyRec.Body.Bytes())
	if len(got.Messages) != 2 {
		t.Fatalf("history messages = %+v", got.Messages)
	}
	gotTextByID := make(map[string]string)
	for _, message := range got.Messages {
		if len(message.Parts) != 1 {
			t.Fatalf("bad parts for history message: %+v", message)
		}
		gotTextByID[message.MessageID] = message.Parts[0].Text
	}
	if gotTextByID["msg_history_1"] != "hello from history" ||
		gotTextByID["msg_history_native"] != "hello from native history" {
		t.Fatalf("bad history response: %+v", got.Messages)
	}
}

func testServer(t *testing.T, ownerEmail string) (*Server, string) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC) }
	store := NewMemoryStore(now)
	srv := newServer(Config{
		LocalUserID:          "local@example.com",
		AllowLocalUserHeader: true,
		AdminEmails:          map[string]bool{"admin@example.com": true},
	}, store)
	srv.clock = now

	tokenReq := httptest.NewRequest(
		http.MethodPost,
		"/api/user/agent-registration-tokens",
		bytes.NewReader([]byte(`{}`)),
	)
	setJSON(tokenReq)
	tokenReq.Header.Set("X-User-Email", ownerEmail)
	tokenRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("registration token code = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	tokenResp := decodeData[CreateRegistrationTokenResponse](t, tokenRec.Body.Bytes())

	req := httptest.NewRequest(http.MethodPost, "/api/agent/register", bytes.NewReader([]byte(`{
		"name":"workstation",
		"hostname":"workstation",
		"agent_type":"hermes",
		"os":"linux"
	}`)))
	setJSON(req)
	req.Header.Set("X-Registration-Token", tokenResp.Token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register code = %d, body = %s", rec.Code, rec.Body.String())
	}
	registered := decodeData[RegisterAgentResponse](t, rec.Body.Bytes())
	if registered.AgentID == "" || registered.APIKey == "" {
		t.Fatalf("bad register response: %+v", registered)
	}
	return srv, registered.APIKey
}

type fakePaxdArtifactBackend struct {
	principal      string
	attrs          paxdArtifactObjectAttrs
	signedArtifact PaxdArtifact
	expiresAt      time.Time
}

func (b *fakePaxdArtifactBackend) SignDownloadURL(
	ctx context.Context,
	artifact PaxdArtifact,
	expiresAt time.Time,
) (string, error) {
	b.signedArtifact = artifact
	b.expiresAt = expiresAt
	return "https://signed.example/" + artifact.Object, nil
}

func (b *fakePaxdArtifactBackend) VerifyUploader(
	ctx context.Context,
	token string,
	audience string,
) (string, error) {
	if token != "valid-token" {
		return "", ErrUnauthorized
	}
	return b.principal, nil
}

func (b *fakePaxdArtifactBackend) ObjectAttrs(
	ctx context.Context,
	bucket string,
	object string,
	generation int64,
) (paxdArtifactObjectAttrs, error) {
	return b.attrs, nil
}

func userPrincipalFromHTTPRequest(
	t *testing.T,
	srv *Server,
	req *http.Request,
) (UserPrincipal, error) {
	t.Helper()
	ctx := srv.engine(":0").NewContext()
	if err := adaptor.CopyToHertzRequest(req, &ctx.Request); err != nil {
		t.Fatal(err)
	}
	return srv.userPrincipal(req.Context(), ctx)
}

func testAgentID(t *testing.T, srv *Server, userEmail string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/user/agents", nil)
	req.Header.Set("X-User-Email", userEmail)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agents code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[struct {
		Agents []Agent `json:"agents"`
	}](t, rec.Body.Bytes())
	if len(got.Agents) != 1 {
		t.Fatalf("agents len = %d", len(got.Agents))
	}
	return got.Agents[0].AgentID
}

func createTestApproval(
	t *testing.T,
	srv *Server,
	paxKey string,
	agentID string,
	fingerprint string,
) string {
	t.Helper()
	body := []byte(`{
		"domain":"agent_action",
		"operation":"session/request_permission",
		"resource_type":"acp_permission",
		"resource_ref":"test",
		"title":"Test permission",
		"action_fingerprint":"` + fingerprint + `",
		"options":[
			{"option_id":"deny","label":"Deny","decision":"deny","scope":"once"},
			{"option_id":"allow_once","label":"Allow once","decision":"allow","scope":"once"}
		]
	}`)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/node/agents/"+agentID+"/approvals",
		bytes.NewReader(body),
	)
	setJSON(req)
	req.Header.Set("X-Pax-Key", paxKey)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create approval code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[struct {
		Approval AgentApproval `json:"approval"`
	}](t, rec.Body.Bytes())
	if got.Approval.ApprovalID == "" {
		t.Fatalf("empty approval response: %+v", got)
	}
	return got.Approval.ApprovalID
}

func decideTestApproval(t *testing.T, srv *Server, approvalID string, option string) {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/approvals/"+approvalID+"/decision",
		bytes.NewReader([]byte(`{"decision_option":"`+option+`"}`)),
	)
	setJSON(req)
	req.Header.Set("X-User-Email", "todd@example.com")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("decide approval code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[struct {
		Approval AgentApproval `json:"approval"`
	}](t, rec.Body.Bytes())
	if got.Approval.DecisionOption != option || got.Approval.Decision != "allow" {
		t.Fatalf("bad approval decision: %+v", got.Approval)
	}
}

func containsACPOption(options []map[string]any, optionID string) bool {
	for _, option := range options {
		if acpOptionID(option) == optionID {
			return true
		}
	}
	return false
}

func reportTestSession(
	t *testing.T,
	srv *Server,
	apiKey string,
	userEmail string,
	agentID string,
	nativeSessionID string,
) string {
	t.Helper()
	body := []byte(`{
		"agent_id":"` + agentID + `",
		"hostname":"workstation",
		"sessions":[{
			"session_id":"` + nativeSessionID + `",
			"name":"test session",
			"status":"running"
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/status", bytes.NewReader(body))
	setJSON(req)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("report session code = %d, body = %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/user/agents/"+agentID+"/sessions", nil)
	req.Header.Set("X-User-Email", userEmail)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list sessions code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := decodeData[struct {
		Sessions []AgentSession `json:"sessions"`
	}](t, rec.Body.Bytes())
	if len(got.Sessions) != 1 {
		t.Fatalf("sessions len = %d", len(got.Sessions))
	}
	if got.Sessions[0].SessionID == "" || got.Sessions[0].SessionID == nativeSessionID {
		t.Fatalf("session was not virtualized: %+v", got.Sessions[0])
	}
	return got.Sessions[0].SessionID
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

func decodeData[T any](t *testing.T, data []byte) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func setJSON(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}

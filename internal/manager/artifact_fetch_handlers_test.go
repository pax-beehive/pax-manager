package manager

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const artifactFetchTestSession = "sess_artifact_fetch"

var artifactFetchPublicationIDPattern = regexp.MustCompile(`^apub_[0-9a-f]{32}$`)

func TestArtifactFetchCheckGivenAllowedPathWhenCheckedThenReturnsPaxdResult(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	paxd := attachArtifactFetchPaxd(t, srv, fixture.nodeID, func(
		frame artifactFetchOutgoingFrame,
	) map[string]any {
		return artifactFetchCheckResponse(frame.RequestID, map[string]any{
			"allowed":    true,
			"path":       "/home/u/report.pdf",
			"filename":   "report.pdf",
			"size_bytes": 1234,
		})
	})

	rec := artifactFetchCheck(t, srv, fixture.userEmail, "/home/u/report.pdf")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"code":200,
		"message":"ok",
		"data":{
			"allowed":true,
			"path":"/home/u/report.pdf",
			"filename":"report.pdf",
			"size_bytes":1234
		}
	}`, rec.Body.String())
	frames := paxd.frames()
	require.Len(t, frames, 1)
	assert.Equal(t, "query", frames[0].Kind)
	assert.JSONEq(t, `{
		"type":"artifact_fetch.check",
		"check_artifact_fetch":{"path":"/home/u/report.pdf"}
	}`, string(frames[0].Query))
}

func TestArtifactFetchCheckGivenDeniedPathWhenCheckedThenPassesDenialThrough(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	attachArtifactFetchPaxd(t, srv, fixture.nodeID, func(
		frame artifactFetchOutgoingFrame,
	) map[string]any {
		return artifactFetchCheckResponse(frame.RequestID, map[string]any{
			"allowed":  false,
			"path":     "/home/u/.ssh/id_ed25519",
			"filename": "id_ed25519",
			"denial": map[string]any{
				"code":    "hidden_path_denied",
				"rule":    "hidden_path",
				"message": "hidden files are not shared",
			},
		})
	})

	rec := artifactFetchCheck(t, srv, fixture.userEmail, "/home/u/.ssh/id_ed25519")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"allowed":false,
		"path":"/home/u/.ssh/id_ed25519",
		"filename":"id_ed25519",
		"denial":{
			"code":"hidden_path_denied",
			"rule":"hidden_path",
			"message":"hidden files are not shared"
		}
	}`, string(artifactFetchData(t, rec)))
}

func TestArtifactFetchCheckGivenGarbageResultWhenCheckedThenReturnsBadGateway(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	attachArtifactFetchPaxd(t, srv, fixture.nodeID, func(
		frame artifactFetchOutgoingFrame,
	) map[string]any {
		return map[string]any{
			"kind":         "response",
			"request_id":   frame.RequestID,
			"query_result": map[string]any{"type": "artifact_fetch.check"},
		}
	})

	rec := artifactFetchCheck(t, srv, fixture.userEmail, "/home/u/report.pdf")

	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

func TestArtifactFetchGivenAppliedAckWhenFetchedThenCreatesUserFetchPublication(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	paxd := attachArtifactFetchPaxd(t, srv, fixture.nodeID, artifactFetchAppliedResponder(
		"report-final.pdf",
		4096,
		nil,
	))

	rec := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/report.pdf"}`)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var data struct {
		Accepted    bool            `json:"accepted"`
		Publication json.RawMessage `json:"publication"`
	}
	require.NoError(t, json.Unmarshal(artifactFetchData(t, rec), &data))
	require.True(t, data.Accepted)
	var publication map[string]any
	require.NoError(t, json.Unmarshal(data.Publication, &publication))
	publicationID, _ := publication["publication_id"].(string)
	require.Regexp(t, artifactFetchPublicationIDPattern, publicationID)
	assert.Equal(t, "user_fetch", publication["origin"])
	assert.Equal(t, "queued", publication["status"])
	assert.Equal(t, "report-final.pdf", publication["filename"])
	assert.Equal(t, fixture.nodeID, publication["node_id"])
	assert.Equal(t, fixture.agentID, publication["agent_id"])
	assert.Equal(t, artifactFetchTestSession, publication["session_id"])
	assert.Equal(t, "/home/u/report.pdf", publication["requested_path"])
	assert.NotContains(t, publication, "title")

	frames := paxd.frames()
	require.Len(t, frames, 1)
	assert.Equal(t, "command", frames[0].Kind)
	assert.Equal(t, publicationID, frames[0].CommandID)
	assert.JSONEq(t, `{
		"command_id":"`+publicationID+`",
		"type":"artifact.fetch",
		"fetch_artifact":{
			"publication_id":"`+publicationID+`",
			"agent_id":"`+fixture.agentID+`",
			"session_id":"`+artifactFetchTestSession+`",
			"path":"/home/u/report.pdf"
		}
	}`, string(frames[0].Command))

	principal := testUserPrincipal(t, srv, fixture.userEmail)
	stored, err := srv.store.GetArtifactPublication(t.Context(), principal, publicationID)
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactPublicationOriginUserFetch, stored.Origin)
	assert.Equal(t, principal.User.UserID, stored.RequestedByUserID)
	assert.Equal(t, "/home/u/report.pdf", stored.RequestedPath)
	assert.Empty(t, stored.Title)

	read := getArtifactPublication(t, srv, fixture.userEmail, publicationID)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	var readData struct {
		Publication json.RawMessage `json:"publication"`
	}
	require.NoError(t, json.Unmarshal(artifactFetchData(t, read), &readData))
	assert.JSONEq(t, string(data.Publication), string(readData.Publication))
}

func TestArtifactFetchGivenRejectedAckWhenFetchedThenReturnsDenialWithoutRow(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	var commandID string
	attachArtifactFetchPaxd(t, srv, fixture.nodeID, func(
		frame artifactFetchOutgoingFrame,
	) map[string]any {
		commandID = frame.CommandID
		return artifactFetchAck(frame.CommandID, map[string]any{
			"ok":     false,
			"status": "rejected",
			"error": map[string]any{
				"code":    "credential_path_denied",
				"message": "credential files are not shared",
				"target":  "credential_path",
			},
		})
	})

	rec := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/.aws/credentials"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"accepted":false,
		"denial":{
			"code":"credential_path_denied",
			"rule":"credential_path",
			"message":"credential files are not shared"
		}
	}`, string(artifactFetchData(t, rec)))
	require.NotEmpty(t, commandID)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, err := srv.store.GetArtifactPublication(t.Context(), principal, commandID)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestArtifactFetchGivenWorkerRegistersFirstWhenFetchedThenRowIsUserFetch(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	var workerStatus int
	attachArtifactFetchPaxd(t, srv, fixture.nodeID, artifactFetchAppliedResponder(
		"report.pdf",
		10,
		func(publicationID string) {
			worker := registerArtifactPublication(
				t,
				srv,
				fixture.nodeAPIKey,
				publicationID,
				`{"source":{"agent_id":"`+fixture.agentID+`","session_id":"`+
					artifactFetchTestSession+`"},"filename":"report.pdf"}`,
			)
			workerStatus = worker.Code
		},
	))

	rec := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/report.pdf"}`)

	require.Equal(t, http.StatusOK, workerStatus)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	publicationID := artifactFetchPublicationID(t, rec)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	stored, err := srv.store.GetArtifactPublication(t.Context(), principal, publicationID)
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactPublicationOriginUserFetch, stored.Origin)
	assert.Equal(t, principal.User.UserID, stored.RequestedByUserID)
	assert.Equal(t, "/home/u/report.pdf", stored.RequestedPath)
}

func TestArtifactFetchGivenWorkerRegistersAfterWhenRegisteredThenKeepsUserFetchRow(
	t *testing.T,
) {
	srv, fixture := artifactFetchFixture(t)
	attachArtifactFetchPaxd(t, srv, fixture.nodeID, artifactFetchAppliedResponder(
		"report.pdf",
		10,
		nil,
	))
	rec := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/report.pdf"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	publicationID := artifactFetchPublicationID(t, rec)

	worker := registerArtifactPublication(
		t,
		srv,
		fixture.nodeAPIKey,
		publicationID,
		`{"source":{"agent_id":"`+fixture.agentID+`","session_id":"`+
			artifactFetchTestSession+`"},"filename":"report.pdf"}`,
	)

	require.Equal(t, http.StatusOK, worker.Code, worker.Body.String())
	assert.Contains(t, worker.Body.String(), `"origin":"user_fetch"`)
	assert.Equal(t, publicationID, decodeArtifactPublication(t, worker).PublicationID)
}

func TestArtifactFetchGivenSessionOfAnotherUserWhenRequestedThenReturnsNotFound(
	t *testing.T,
) {
	srv, fixture := artifactFetchFixture(t)
	other := testNodeAgent(t, srv, "fetch-other@example.com")
	createArtifactPublicationSession(t, srv, other, "sess_artifact_fetch_other")
	paxd := attachArtifactFetchPaxd(t, srv, other.nodeID, artifactFetchAppliedResponder(
		"x.txt", 1, nil,
	))

	check := artifactFetchRequest(
		t, srv, fixture.userEmail, http.MethodGet,
		"/api/v1/user/self/sessions/sess_artifact_fetch_other/artifact-fetches/check?path="+
			url.QueryEscape("/home/u/x.txt"),
		"",
	)
	post := artifactFetchRequest(
		t, srv, fixture.userEmail, http.MethodPost,
		"/api/v1/user/self/sessions/sess_artifact_fetch_other/artifact-fetches",
		`{"path":"/home/u/x.txt"}`,
	)
	missing := artifactFetchRequest(
		t, srv, fixture.userEmail, http.MethodPost,
		"/api/v1/user/self/sessions/sess_missing/artifact-fetches",
		`{"path":"/home/u/x.txt"}`,
	)

	require.Equal(t, http.StatusNotFound, check.Code, check.Body.String())
	require.Equal(t, http.StatusNotFound, post.Code, post.Body.String())
	require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
	require.Empty(t, paxd.frames())
}

func TestArtifactFetchGivenNodeOfflineWhenRequestedThenReturnsServiceUnavailable(
	t *testing.T,
) {
	srv, fixture := artifactFetchFixture(t)

	check := artifactFetchCheck(t, srv, fixture.userEmail, "/home/u/report.pdf")
	post := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/report.pdf"}`)

	require.Equal(t, http.StatusServiceUnavailable, check.Code, check.Body.String())
	require.Equal(t, http.StatusServiceUnavailable, post.Code, post.Body.String())
}

func TestArtifactFetchGivenInvalidPathWhenRequestedThenReturnsBadRequest(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	paxd := attachArtifactFetchPaxd(t, srv, fixture.nodeID, artifactFetchAppliedResponder(
		"x.txt", 1, nil,
	))
	invalidPaths := map[string]string{
		"empty":    "",
		"relative": "docs/report.pdf",
		"newline":  "/home/u/a\nb",
		"carriage": "/home/u/a\rb",
		"nul":      "/home/u/a\x00b",
		"too long": "/" + strings.Repeat("a", 8192),
	}
	for name, path := range invalidPaths {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"path": path})
			require.NoError(t, err)

			check := artifactFetchCheck(t, srv, fixture.userEmail, path)
			post := artifactFetchPost(t, srv, fixture.userEmail, string(body))

			require.Equal(t, http.StatusBadRequest, check.Code, check.Body.String())
			require.Equal(t, http.StatusBadRequest, post.Code, post.Body.String())
		})
	}
	for name, body := range map[string]string{
		"non json":     `path=/home/u/x.txt`,
		"missing path": `{}`,
		"wrong type":   `{"path":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			post := artifactFetchPost(t, srv, fixture.userEmail, body)
			require.Equal(t, http.StatusBadRequest, post.Code, post.Body.String())
		})
	}
	missingQuery := artifactFetchRequest(
		t, srv, fixture.userEmail, http.MethodGet,
		"/api/v1/user/self/sessions/"+artifactFetchTestSession+"/artifact-fetches/check",
		"",
	)
	require.Equal(t, http.StatusBadRequest, missingQuery.Code, missingQuery.Body.String())
	require.Empty(t, paxd.frames())
}

func TestArtifactFetchGivenEncryptedSessionWhenRequestedThenReturnsConflict(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	_, _, err := srv.store.CreateAgentCommand(t.Context(), domain.AgentCommand{
		E2EERecord: domain.E2EERecord{
			RecordID: "cmd_e2ee_fetch", OwnerUserID: principal.User.UserID,
			NodeID: fixture.nodeID, AgentID: fixture.agentID,
			SessionID: artifactFetchTestSession, Kind: "acp_command",
			ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1,
			Nonce:      []byte("123456789012"),
			Ciphertext: []byte("0123456789abcdef"), CreatedAt: time.Now().UTC(),
		},
	})
	require.NoError(t, err)
	paxd := attachArtifactFetchPaxd(t, srv, fixture.nodeID, artifactFetchAppliedResponder(
		"x.txt", 1, nil,
	))

	check := artifactFetchCheck(t, srv, fixture.userEmail, "/home/u/report.pdf")
	post := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/report.pdf"}`)

	require.Equal(t, http.StatusConflict, check.Code, check.Body.String())
	require.Equal(t, http.StatusConflict, post.Code, post.Body.String())
	require.Empty(t, paxd.frames())
}

func TestArtifactFetchGivenSilentPaxdWhenRequestedThenReturnsGatewayTimeout(t *testing.T) {
	srv, fixture := artifactFetchFixture(t)
	original := artifactFetchControlTimeout
	artifactFetchControlTimeout = 50 * time.Millisecond
	t.Cleanup(func() { artifactFetchControlTimeout = original })
	attachArtifactFetchPaxd(
		t,
		srv,
		fixture.nodeID,
		func(artifactFetchOutgoingFrame) map[string]any {
			return nil
		},
	)

	check := artifactFetchCheck(t, srv, fixture.userEmail, "/home/u/report.pdf")
	post := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/report.pdf"}`)

	require.Equal(t, http.StatusGatewayTimeout, check.Code, check.Body.String())
	require.Equal(t, http.StatusGatewayTimeout, post.Code, post.Body.String())
}

func TestArtifactFetchGivenAppliedAckWithUnsafeFilenameWhenFetchedThenReturnsBadGateway(
	t *testing.T,
) {
	srv, fixture := artifactFetchFixture(t)
	attachArtifactFetchPaxd(t, srv, fixture.nodeID, artifactFetchAppliedResponder(
		"../escape.txt", 1, nil,
	))

	rec := artifactFetchPost(t, srv, fixture.userEmail, `{"path":"/home/u/escape.txt"}`)

	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

type artifactFetchOutgoingFrame struct {
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	CommandID string          `json:"command_id"`
	Query     json.RawMessage `json:"query"`
	Command   json.RawMessage `json:"command"`
}

type artifactFetchFakePaxd struct {
	mu       sync.Mutex
	received []artifactFetchOutgoingFrame
}

func (p *artifactFetchFakePaxd) frames() []artifactFetchOutgoingFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]artifactFetchOutgoingFrame(nil), p.received...)
}

// attachArtifactFetchPaxd connects a fake paxd control tunnel for nodeID. The
// responder returns the full incoming frame, or nil to stay silent.
func attachArtifactFetchPaxd(
	t *testing.T,
	srv *Server,
	nodeID string,
	respond func(artifactFetchOutgoingFrame) map[string]any,
) *artifactFetchFakePaxd {
	t.Helper()
	ws := newFakeNodeControlWebSocket()
	conn := newNodeControlConnection(nodeID, ws)
	srv.nodeControls.Add(nodeID, conn)
	paxd := &artifactFetchFakePaxd{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var payload []byte
			select {
			case <-ws.closed:
				return
			case payload = <-ws.writes:
			}
			var frame artifactFetchOutgoingFrame
			if json.Unmarshal(payload, &frame) != nil {
				continue
			}
			paxd.mu.Lock()
			paxd.received = append(paxd.received, frame)
			paxd.mu.Unlock()
			response := respond(frame)
			if response == nil {
				continue
			}
			raw, err := json.Marshal(response)
			if err != nil {
				continue
			}
			_, _ = conn.HandleIncoming(raw)
		}
	}()
	t.Cleanup(func() {
		srv.nodeControls.Remove(nodeID, conn)
		_ = ws.Close()
		<-done
	})
	return paxd
}

func artifactFetchAppliedResponder(
	filename string,
	sizeBytes int64,
	beforeAck func(publicationID string),
) func(artifactFetchOutgoingFrame) map[string]any {
	return func(frame artifactFetchOutgoingFrame) map[string]any {
		if frame.Kind != "command" {
			return nil
		}
		if beforeAck != nil {
			beforeAck(frame.CommandID)
		}
		return artifactFetchAck(frame.CommandID, map[string]any{
			"ok":          true,
			"status":      "applied",
			"target_type": "artifact_publication",
			"target_id":   frame.CommandID,
			"result": map[string]any{
				"artifact_fetch": map[string]any{
					"publication_id": frame.CommandID,
					"status":         "snapshotting",
					"filename":       filename,
					"size_bytes":     sizeBytes,
				},
			},
		})
	}
}

func artifactFetchAck(commandID string, ack map[string]any) map[string]any {
	ack["command_id"] = commandID
	return map[string]any{
		"kind":        "ack",
		"command_id":  commandID,
		"command_ack": ack,
	}
}

func artifactFetchCheckResponse(requestID string, check map[string]any) map[string]any {
	return map[string]any{
		"kind":       "response",
		"request_id": requestID,
		"query_result": map[string]any{
			"type":                 "artifact_fetch.check",
			"artifact_fetch_check": check,
		},
	}
}

func artifactFetchFixture(t *testing.T) (*Server, conversationTestFixture) {
	t.Helper()
	srv, _ := testServer(t, "artifact-fetch@example.com")
	fixture := testNodeAgent(t, srv, "artifact-fetch@example.com")
	createArtifactPublicationSession(t, srv, fixture, artifactFetchTestSession)
	return srv, fixture
}

func artifactFetchCheck(
	t *testing.T,
	srv *Server,
	email string,
	path string,
) *httptest.ResponseRecorder {
	t.Helper()
	return artifactFetchRequest(
		t, srv, email, http.MethodGet,
		"/api/v1/user/self/sessions/"+artifactFetchTestSession+
			"/artifact-fetches/check?path="+url.QueryEscape(path),
		"",
	)
}

func artifactFetchPost(
	t *testing.T,
	srv *Server,
	email string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return artifactFetchRequest(
		t, srv, email, http.MethodPost,
		"/api/v1/user/self/sessions/"+artifactFetchTestSession+"/artifact-fetches",
		body,
	)
}

func artifactFetchRequest(
	t *testing.T,
	srv *Server,
	email string,
	method string,
	target string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	req.Header.Set("X-User-Email", email)
	if body != "" {
		setJSON(req)
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func artifactFetchData(t *testing.T, rec *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Data
}

func artifactFetchPublicationID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var data struct {
		Publication struct {
			PublicationID string `json:"publication_id"`
		} `json:"publication"`
	}
	require.NoError(t, json.Unmarshal(artifactFetchData(t, rec), &data))
	require.Regexp(t, artifactFetchPublicationIDPattern, data.Publication.PublicationID)
	return data.Publication.PublicationID
}

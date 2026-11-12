package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const queueFilesSession = "sess-queue-files"

func createCompletedTestAttachment(
	t *testing.T,
	srv *Server,
	principal UserPrincipal,
	filename, contentType string,
) domain.UserAttachment {
	t.Helper()
	attachment, err := srv.store.CreateUserAttachment(
		t.Context(),
		principal,
		CreateUserAttachmentRequest{Filename: filename, ContentType: contentType, SizeBytes: 5},
		"attachments",
		"user-attachments/"+principal.User.UserID+"/"+filename,
		time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	attachment, err = srv.store.CompleteUserAttachment(
		t.Context(),
		principal,
		attachment.AttachmentID,
		ArtifactContent{ContentType: contentType, SizeBytes: 5, Generation: 1},
	)
	require.NoError(t, err)
	return attachment
}

func markAttachmentLocal(srv *Server, nodeID string, attachment domain.UserAttachment) string {
	localURI := "file:///tmp/" + nodeID + "/" + attachment.AttachmentID + "/" + attachment.Filename
	srv.nodeControls.ObserveAttachmentState(nodeID, nodeControlAttachmentLocalState{
		AttachmentID: attachment.AttachmentID,
		State:        "ready",
		LocalURI:     localURI,
	})
	return localURI
}

func queuedFileOf(attachment domain.UserAttachment) domain.QueuedTurnAttachment {
	return domain.QueuedTurnAttachment{
		AttachmentID: attachment.AttachmentID,
		Filename:     attachment.Filename,
		ContentType:  attachment.ContentType,
		SizeBytes:    attachment.SizeBytes,
	}
}

func attachmentContent(text string, attachmentIDs ...string) string {
	blocks := []map[string]string{}
	if text != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": text})
	}
	for _, id := range attachmentIDs {
		blocks = append(blocks, map[string]string{"type": "attachment", "attachment_id": id})
	}
	raw, _ := json.Marshal(map[string]any{"content": blocks})
	return string(raw)
}

func setupQueueFilesSession(
	t *testing.T,
	lifecycle string,
) (*Server, conversationTestFixture, UserPrincipal, string) {
	t.Helper()
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	createConversationTestSession(t, srv, fixture, queueFilesSession, "native-queue-files")
	require.NoError(t, srv.store.UpdateSessionRuntimeState(t.Context(), domain.SessionRuntimeState{
		NodeID:                fixture.nodeID,
		AgentID:               fixture.agentID,
		SessionID:             queueFilesSession,
		Lifecycle:             lifecycle,
		ActivePromptRequestID: "7",
		ActiveTurnID:          "7",
		UpdatedAt:             time.Now().UTC(),
	}))
	base := "/api/v1/user/self/agents/" + fixture.agentID + "/sessions/" + queueFilesSession + "/turn/"
	return srv, fixture, testUserPrincipal(t, srv, fixture.userEmail), base
}

func TestConversationTurnQueueGivenAttachmentsThenStoresFilesAndKeepsThemOnEdit(t *testing.T) {
	srv, fixture, principal, base := setupQueueFilesSession(t, domain.RuntimeLifecycleRunning)
	notes := createCompletedTestAttachment(t, srv, principal, "notes.txt", "text/plain")
	markAttachmentLocal(srv, fixture.nodeID, notes)
	body := attachmentContent("read this", notes.AttachmentID, notes.AttachmentID)

	post := doConversationTurnQueueRequest(
		t,
		srv,
		fixture,
		http.MethodPost,
		base+"queue",
		"cmd_files",
		body,
	)
	require.Equal(t, http.StatusOK, post.Code, post.Body.String())
	queuedResponse := decodeData[conversationTurnQueueResponse](t, post.Body.Bytes())
	assert.Equal(t, []domain.QueuedTurnAttachment{queuedFileOf(notes)}, queuedResponse.Attachments,
		"the response echoes the stored files")
	get := doConversationTurnQueueRequest(t, srv, fixture, http.MethodGet, base+"queue", "", "")
	got := decodeData[conversationQueuedTurn](t, get.Body.Bytes())
	assert.Equal(t, "read this", got.Input)
	assert.Equal(t, []domain.QueuedTurnAttachment{queuedFileOf(notes)}, got.Attachments,
		"duplicate attachment blocks queue the file once")

	patch := doConversationTurnQueueRequest(
		t, srv, fixture, http.MethodPatch, base+"queue", "cmd_edit", `{"input":"read this file"}`,
	)
	require.Equal(t, http.StatusOK, patch.Code, patch.Body.String())
	edited := decodeData[conversationQueuedTurn](t, patch.Body.Bytes())
	assert.Equal(t, "read this file", edited.Input)
	assert.Equal(t, got.Attachments, edited.Attachments)

	rejected := doConversationTurnQueueRequest(
		t, srv, fixture, http.MethodPatch, base+"queue", "cmd_edit_files", body,
	)
	assert.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
}

func TestConversationTurnQueueGivenOnlyAttachmentsThenQueuesThePrompt(t *testing.T) {
	srv, fixture, principal, base := setupQueueFilesSession(t, domain.RuntimeLifecycleRunning)
	shot := createCompletedTestAttachment(t, srv, principal, "shot.png", "image/png")
	markAttachmentLocal(srv, fixture.nodeID, shot)

	post := doConversationTurnQueueRequest(
		t,
		srv,
		fixture,
		http.MethodPost,
		base+"queue",
		"cmd_files",
		attachmentContent("", shot.AttachmentID),
	)
	require.Equal(t, http.StatusOK, post.Code, post.Body.String())
	queued, ok, err := srv.conversationTurns.get(t.Context(), fixture.agentID, queueFilesSession)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, queued.Input)
	assert.Equal(t, []domain.QueuedTurnAttachment{queuedFileOf(shot)}, queued.Attachments)

	empty := doConversationTurnQueueRequest(
		t, srv, fixture, http.MethodPost, base+"queue", "cmd_empty", `{"content":[]}`,
	)
	assert.Equal(t, http.StatusBadRequest, empty.Code)
}

func TestConversationTurnQueueGivenUnusableAttachmentsThenNothingIsQueued(t *testing.T) {
	srv, fixture, principal, base := setupQueueFilesSession(t, domain.RuntimeLifecycleRunning)
	other := testUserPrincipal(t, srv, "someone-else@example.com")
	foreign := createCompletedTestAttachment(t, srv, other, "secret.txt", "text/plain")
	pending, err := srv.store.CreateUserAttachment(
		t.Context(),
		principal,
		CreateUserAttachmentRequest{Filename: "half.txt", ContentType: "text/plain", SizeBytes: 5},
		"attachments",
		"user-attachments/"+principal.User.UserID+"/half.txt",
		time.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	tooMany := make([]string, domain.MaxQueuedTurnAttachments+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("att_%d", i)
	}

	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"another user's file": {attachmentContent("x", foreign.AttachmentID), http.StatusNotFound},
		"unfinished upload":   {attachmentContent("x", pending.AttachmentID), http.StatusConflict},
		"too many files":      {attachmentContent("x", tooMany...), http.StatusBadRequest},
		"malformed block":     {`{"content":[{"type":"attachment"}]}`, http.StatusBadRequest},
	} {
		resp := doConversationTurnQueueRequest(
			t, srv, fixture, http.MethodPost, base+"queue", "cmd_"+name, tc.body,
		)
		assert.Equal(t, tc.status, resp.Code, "%s: %s", name, resp.Body.String())
	}
	_, ok, err := srv.conversationTurns.get(t.Context(), fixture.agentID, queueFilesSession)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestConversationTurnSteerGivenAttachmentsThenQueuesFilesWithTheSteeringPrompt(
	t *testing.T,
) {
	// A cancelling turn is still active, and stopping it needs no agent tunnel.
	srv, fixture, principal, base := setupQueueFilesSession(t, domain.RuntimeLifecycleCancelling)
	shot := createCompletedTestAttachment(t, srv, principal, "shot.png", "image/png")
	markAttachmentLocal(srv, fixture.nodeID, shot)

	req := httptest.NewRequest(
		http.MethodPost,
		base+"steer",
		strings.NewReader(attachmentContent("use this instead", shot.AttachmentID)),
	)
	req.Header.Set("X-User-Email", fixture.userEmail)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "cmd_steer")
	rec := httptest.NewRecorder()
	srv.handleConversationTurnSteer(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	steer := decodeData[conversationTurnSteerResponse](t, rec.Body.Bytes())
	assert.Equal(t, turnStopEffectAlreadyCancelling, steer.StopEffect)
	assert.Equal(t, []domain.QueuedTurnAttachment{queuedFileOf(shot)}, steer.Attachments)

	queued, ok, err := srv.conversationTurns.get(t.Context(), fixture.agentID, queueFilesSession)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "use this instead", queued.Input)
	assert.Equal(t, []domain.QueuedTurnAttachment{queuedFileOf(shot)}, queued.Attachments)
}

func TestQueuedTurnWithAttachmentsSendsLocalResourceLinks(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	createConversationTestSession(t, srv, fixture, "queued-session", "queued-native")
	reportQueueTestSnapshot(t, srv, fixture, 1, nil)
	require.Eventually(
		t,
		func() bool { _, busy := srv.queueChecks.Load(fixture.agentID); return !busy },
		time.Second,
		time.Millisecond,
	)
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

	principal := testUserPrincipal(t, srv, fixture.userEmail)
	notes := createCompletedTestAttachment(t, srv, principal, "notes.txt", "text/plain")
	localURI := markAttachmentLocal(srv, fixture.nodeID, notes)
	turn, _, err := srv.conversationTurns.upsert(t.Context(), conversationQueuedTurn{
		AgentID:     fixture.agentID,
		SessionID:   "queued-session",
		OwnerID:     principal.User.UserID,
		CommandID:   "queue-command",
		Input:       "summarize the notes",
		Attachments: []domain.QueuedTurnAttachment{queuedFileOf(notes)},
	})
	require.NoError(t, err)

	reportQueueTestSnapshot(t, srv, fixture, 2, nil)
	prompt := readNextManagerToAgentData(t, ws)
	require.Equal(t, turn.TurnID, prompt.Metadata["turn_id"])
	var request struct {
		Params struct {
			Prompt []map[string]any `json:"prompt"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(prompt.Payload, &request))
	require.Equal(t, []map[string]any{
		{"type": "text", "text": "summarize the notes"},
		{"type": "resource_link", "uri": localURI, "name": "notes.txt", "mimeType": "text/plain"},
	}, request.Params.Prompt)
}

func TestQueuedTurnWithUnavailableAttachmentIsNotSentWithoutIt(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	createConversationTestSession(t, srv, fixture, "queued-session", "queued-native")
	reportQueueTestSnapshot(t, srv, fixture, 1, nil)
	require.Eventually(
		t,
		func() bool { _, busy := srv.queueChecks.Load(fixture.agentID); return !busy },
		time.Second,
		time.Millisecond,
	)
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

	principal := testUserPrincipal(t, srv, fixture.userEmail)
	// The file was deleted, or never belonged to this owner, after queuing.
	_, _, err = srv.conversationTurns.upsert(t.Context(), conversationQueuedTurn{
		AgentID:   fixture.agentID,
		SessionID: "queued-session",
		OwnerID:   principal.User.UserID,
		CommandID: "queue-command",
		Input:     "summarize the notes",
		Attachments: []domain.QueuedTurnAttachment{{
			AttachmentID: "att_missing", Filename: "notes.txt",
		}},
	})
	require.NoError(t, err)

	reportQueueTestSnapshot(t, srv, fixture, 2, nil)
	require.Eventually(t, func() bool {
		queued, ok, err := srv.conversationTurns.get(t.Context(), fixture.agentID, "queued-session")
		return err == nil && ok && queued.State == "uncertain"
	}, 5*time.Second, 10*time.Millisecond, "the turn stays visible as unconfirmed")
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
	_, _, err = ws.ReadMessage()
	assert.Error(t, err, "no prompt is sent without the files")
}

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationSlotRetryOnlyAcceptsExplicitPreDispatchRejection(t *testing.T) {
	for _, tt := range []struct {
		errorJSON string
		retry     bool
	}{
		{`{"code":-32003,"data":{"kind":"slot_unavailable","retryable":true}}`, true},
		{`{"code":"-32003","data":{"kind":"slot_unavailable","retryable":true}}`, false},
		{`{"data":{"kind":"slot_unavailable","retryable":true}}`, false},
		{`{"code":-32603,"data":{"kind":"slot_unavailable","retryable":true}}`, false},
		{`{"code":-32003,"data":{"kind":"slot_busy","retryable":true}}`, false},
		{`{"code":-32003,"data":{"kind":"daemon_restarting","retryable":true}}`, false},
		{`{"code":-32003,"data":{"kind":"slot_unavailable","retryable":false}}`, false},
		{`{"code":-32003,"data":{"kind":"slot_unavailable"}}`, false},
		{`{"message":"no ready ACP slot"}`, false},
	} {
		assert.Equal(t, tt.retry, isUndispatchedSessionNew(decodeConversationACPError(
			json.RawMessage(tt.errorJSON),
		)), tt.errorJSON)
	}
	assert.False(t, isUndispatchedSessionNew(context.DeadlineExceeded))
}

func TestConversationSlotRetryCanReconnectAndSendOnePrompt(t *testing.T) {
	srv, _ := testServer(t, "startup@example.test")
	fixture := testNodeAgent(t, srv, "startup@example.test", "pi")
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- postConversationRecorder(t, srv, fixture, `{"input":"one prompt"}`)
	}()

	first, firstRequest := readMockACPRequest(t, ws, "session/new")
	sessionID := first.Metadata["manager_session_id"]
	require.NotEmpty(t, sessionID)
	writeColdSlotRejection(t, ws, first, 1, firstRequest)
	oldConn, err := srv.acpTunnels.findAny(fixture.agentID, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return oldConn.liveState().sessionMux.activeTurnCount() == 0
	}, time.Second, 5*time.Millisecond)
	require.NoError(t, ws.Close())

	// A replacement tunnel for this same agent can serve the new attempt.
	ws = secretarySlotRetryTunnel(t, srv, fixture)
	next, nextRequest := readMockACPRequest(t, ws, "session/new")
	newConn, err := srv.acpTunnels.findAny(fixture.agentID, "")
	require.NoError(t, err)
	assert.NotSame(t, oldConn, newConn)
	assert.Equal(t, sessionID, next.Metadata["manager_session_id"])
	assert.JSONEq(t, string(firstRequest.Params), string(nextRequest.Params))
	assert.NotEqual(t, firstRequest.ID, nextRequest.ID)
	// A redelivered old refusal must not cause a third attempt.
	writeColdSlotRejection(t, ws, next, 2, firstRequest)
	writeMockACPResponse(
		t,
		ws,
		next.QueueID,
		3,
		nextRequest.ID,
		json.RawMessage(`{"sessionId":"native-pi"}`),
	)
	prompt, request := readMockACPRequest(t, ws, "session/prompt")
	assert.Equal(t, sessionID, prompt.Metadata["manager_session_id"])
	assert.Contains(t, string(request.Params), "one prompt")
	writeMockACPResponse(
		t,
		ws,
		prompt.QueueID,
		4,
		request.ID,
		json.RawMessage(`{"stopReason":"end_turn"}`),
	)
	select {
	case rec := <-response:
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		events := decodeConversationEvents(t, rec.Body.Bytes())
		assert.Equal(t, sessionID, requireConversationEvent(t, events, "session").SessionID)
		requireConversationEvent(t, events, "done")
	case <-time.After(5 * time.Second):
		t.Fatal("the user prompt did not finish")
	}
	// Only two session/new requests and one prompt have consumed wire IDs.
	nextID, err := srv.store.NextAgentACPRequestID(t.Context(), fixture.agentID)
	require.NoError(t, err)
	assert.EqualValues(t, 4, nextID)
}

func TestConversationSlotRetryCancellationReleasesClaimWithoutAnotherSend(t *testing.T) {
	srv, _ := testServer(t, "startup@example.test")
	fixture := testNodeAgent(t, srv, "startup@example.test", "pi")
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost,
		"/api/v1/user/self/nodes/"+fixture.nodeID+"/agents/"+fixture.agentID+"/conversation",
		strings.NewReader(`{"input":"cancel this"}`))
	req.Header.Set("X-User-Email", fixture.userEmail)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleConversation(httptest.NewRecorder(), req)
	}()
	packet, request := readMockACPRequest(t, ws, "session/new")
	writeColdSlotRejection(t, ws, packet, 1, request)
	conn, err := srv.acpTunnels.findAny(fixture.agentID, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return conn.liveState().sessionMux.activeTurnCount() == 0
	}, time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request did not stop when the caller canceled")
	}
	nextID, err := srv.store.NextAgentACPRequestID(t.Context(), fixture.agentID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, nextID)
}

func writeColdSlotRejection(
	t *testing.T,
	ws *websocket.Conn,
	frame acpTunnelEnvelope,
	seq int64,
	req acpJSONRPCMessage,
) {
	t.Helper()
	writeMockACPError(t, ws, frame.QueueID, seq, req.ID, -32003, "cold", "slot_unavailable", true)
}

func TestConversationSlotRetryInitializeOnlySurvivesRepeatedRejection(t *testing.T) {
	srv, _ := testServer(t, "startup@example.test")
	fixture := testNodeAgent(t, srv, "startup@example.test", "pi")
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	httpServer := httptest.NewServer(http.HandlerFunc(srv.handleConversation))
	defer httpServer.Close()
	respCh, errCh := make(chan *http.Response, 1), make(chan error, 1)
	go postConversation(t, httpServer.URL, fixture, `{"initialize_only":true}`, respCh, errCh)

	first, req := readMockACPRequest(t, ws, "session/new")
	writeColdSlotRejection(t, ws, first, 1, req)
	second, req := readMockACPRequest(t, ws, "session/new")
	writeColdSlotRejection(t, ws, second, 2, req)
	third, req := readMockACPRequest(t, ws, "session/new")
	writeMockACPResponse(
		t,
		ws,
		third.QueueID,
		3,
		req.ID,
		json.RawMessage(`{"sessionId":"native-empty"}`),
	)
	body := readConversationResponse(t, respCh, errCh, http.StatusOK)
	events := decodeConversationEvents(t, body)
	assert.Equal(
		t,
		first.Metadata["manager_session_id"],
		requireConversationEvent(t, events, "session").SessionID,
	)
	requireConversationEvent(t, events, "done")

	nextID, err := srv.store.NextAgentACPRequestID(t.Context(), fixture.agentID)
	require.NoError(t, err)
	assert.EqualValues(t, 4, nextID, "only three session/new requests, no prompt")
}

func TestConversationSlotRetryDeadlineDistinguishesUnsentFromUnconfirmed(t *testing.T) {
	rejection := conversationACPError{code: "-32003", kind: "slot_unavailable", retryable: true}
	runner := conversationRunner{}
	_, err := runner.retryNewConversationSession(t.Context(), nil, rejection, time.Now())
	require.Equal(t, rejection, err)

	srv, _ := testServer(t, "startup@example.test")
	fixture := testNodeAgent(t, srv, "startup@example.test", "pi")
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	runner.agentConn, err = srv.acpTunnels.findAny(fixture.agentID, "")
	require.NoError(t, err)
	runner.managerSessionID = "session-unconfirmed"
	runner.service = srv
	done := make(chan error, 1)
	go func() {
		_, err := runner.retryNewConversationSession(
			t.Context(), map[string]any{"cwd": "/tmp", "mcpServers": []any{}}, rejection,
			time.Now().Add(2*time.Second),
		)
		runner.releaseConversationClaim()
		done <- err
	}()
	// The retry is delivered, but the replacement daemon never confirms it.
	_, _ = readMockACPRequest(t, ws, "session/new")
	select {
	case err := <-done:
		status, message := endpointErrorStatus(err)
		assert.Equal(t, http.StatusGatewayTimeout, status)
		assert.Contains(t, message, "outcome is unknown")
	case <-time.After(4 * time.Second):
		t.Fatal("unconfirmed request exceeded the slot-wait deadline")
	}
	nextID, err := srv.store.NextAgentACPRequestID(t.Context(), fixture.agentID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, nextID)
}

func TestConversationSlotRetryRejectsChangedTunnelIdentity(t *testing.T) {
	for _, next := range []*ACPTunnelAgent{
		{agentID: "agent", nodeID: "wrong-node", ownerUserID: "owner"},
		{agentID: "agent", nodeID: "node", ownerUserID: "wrong-owner"},
	} {
		srv, _ := testServer(t, "startup@example.test")
		srv.acpTunnels.add("agent", "", next)
		r := conversationRunner{
			service: srv, managerSessionID: "session",
			agentConn: &ACPTunnelAgent{agentID: "agent", nodeID: "node", ownerUserID: "owner"},
		}
		err := r.reclaimConversationSlot(t.Context(), time.Now().Add(time.Second))
		status, message := endpointErrorStatus(err)
		require.Equal(t, http.StatusConflict, status)
		assert.Contains(t, message, "identity changed")
		assert.Zero(t, next.liveState().sessionMux.activeTurnCount())
	}
}

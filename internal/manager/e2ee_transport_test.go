package manager

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestE2EECommandEndpointPersistsOpaquePayloadIdempotently(t *testing.T) {
	srv, _ := testServer(t, "e2ee@example.com")
	fixture := testNodeAgent(t, srv, "e2ee@example.com")
	createConversationTestSession(t, srv, fixture, "sess_e2ee", "native_e2ee")
	body := mustE2EEEnvelopeJSON(t, e2eeEnvelope{
		ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1,
		RecordID: "cmd_e2ee_1", AgentID: fixture.agentID,
		SessionID: "sess_e2ee", Kind: "acp_command",
		Nonce:   base64.StdEncoding.EncodeToString([]byte("123456789012")),
		Payload: base64.StdEncoding.EncodeToString([]byte("0123456789abcdefciphertext")),
	})
	path := "/api/v1/user/self/agents/" + fixture.agentID + "/sessions/sess_e2ee/encrypted-commands"

	for index, wantCreated := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-Email", fixture.userEmail)
		rec := httptest.NewRecorder()
		srv.handleE2EECommands(rec, req)
		require.Equal(t, http.StatusAccepted, rec.Code, "attempt %d: %s", index, rec.Body.String())
		var response apiResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		data := response.Data.(map[string]any)
		assert.Equal(t, wantCreated, data["created"])
		assert.Equal(t, "cmd_e2ee_1", data["command_id"])
	}

	pending, err := srv.store.ListPendingAgentCommands(t.Context(), fixture.agentID, 0, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, []byte("0123456789abcdefciphertext"), pending[0].Ciphertext)
}

func TestE2EEEventEndpointResumesAfterLastEventID(t *testing.T) {
	srv, _ := testServer(t, "events@example.com")
	fixture := testNodeAgent(t, srv, "events@example.com")
	createConversationTestSession(t, srv, fixture, "sess_events", "native_events")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	for _, recordID := range []string{"evt_1", "evt_2"} {
		_, _, err := srv.store.InsertAgentEvent(
			t.Context(),
			domain.AgentEvent{E2EERecord: domain.E2EERecord{
				RecordID: recordID, OwnerUserID: principal.User.UserID,
				NodeID: fixture.nodeID, AgentID: fixture.agentID,
				SessionID: "sess_events", Kind: "acp_event",
				ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1,
				Nonce: []byte("123456789012"), Ciphertext: []byte("0123456789abcdef"),
				CreatedAt: time.Now().UTC(),
			}},
		)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/agents/"+fixture.agentID+"/sessions/sess_events/encrypted-events",
		nil,
	).WithContext(ctx)
	req.Header.Set("X-User-Email", fixture.userEmail)
	req.Header.Set("Last-Event-ID", "1")
	rec := &flushSignalRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          make(chan struct{}, 1),
	}
	done := make(chan struct{})
	go func() {
		srv.handleE2EEEvents(rec, req)
		close(done)
	}()
	select {
	case <-rec.flushed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for encrypted event flush")
	}
	cancel()
	<-done

	assert.NotContains(t, rec.Body.String(), "evt_1")
	assert.Contains(t, rec.Body.String(), "evt_2")
}

type flushSignalRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (r *flushSignalRecorder) Flush() {
	r.ResponseRecorder.Flush()
	select {
	case r.flushed <- struct{}{}:
	default:
	}
}

func TestE2EEWakeRegistryCoalescesNotifications(t *testing.T) {
	t.Parallel()
	hub := newE2EEWakeRegistry()
	wake, unsubscribe := hub.subscribe("session_1")
	defer unsubscribe()

	hub.wake("session_1")
	hub.wake("session_1")
	assert.Len(t, wake, 1)
	hub.wakeAll()
	assert.Len(t, wake, 1)
}

func TestE2EEAgentPayloadAcknowledgesCommandsAndDeduplicatesEvents(t *testing.T) {
	t.Parallel()
	store := storage.NewMemoryStore(time.Now)
	epoch, err := store.RegisterAgentConnection(t.Context(), "agent_1")
	require.NoError(t, err)
	command, _, err := store.CreateAgentCommand(
		t.Context(),
		domain.AgentCommand{E2EERecord: domain.E2EERecord{
			RecordID: "cmd_1", OwnerUserID: "user_1", NodeID: "node_1", AgentID: "agent_1",
			SessionID: "session_1", Kind: "acp_command", ProtocolVersion: 1,
			CipherVersion: 1, KeyEpoch: 1, Nonce: []byte("123456789012"),
			Ciphertext: []byte("0123456789abcdef"), CreatedAt: time.Now().UTC(),
		}},
	)
	require.NoError(t, err)
	agent := &ACPTunnelAgent{
		agentID: "agent_1", nodeID: "node_1", ownerUserID: "user_1", store: store,
		connectionEpoch: epoch, eventWakes: newE2EEWakeRegistry(),
	}

	ack, err := json.Marshal(e2eeCommandAck{
		Type: "e2ee_command_ack", CommandID: command.RecordID, ConnectionEpoch: epoch,
	})
	require.NoError(t, err)
	handled, err := agent.handleE2EEAgentPayload(t.Context(), ack)
	require.NoError(t, err)
	assert.True(t, handled)
	pending, err := store.ListPendingAgentCommands(t.Context(), "agent_1", epoch, 100)
	require.NoError(t, err)
	assert.Empty(t, pending)

	eventPayload, err := json.Marshal(encodeE2EERecord(domain.E2EERecord{
		RecordID: "evt_1", AgentID: "agent_1", SessionID: "session_1", Kind: "acp_event",
		ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1,
		Nonce: []byte("123456789012"), Ciphertext: []byte("0123456789abcdef"),
	}))
	require.NoError(t, err)
	for range 2 {
		handled, err = agent.handleE2EEAgentPayload(t.Context(), eventPayload)
		require.NoError(t, err)
		assert.True(t, handled)
	}
	events, err := store.ListAgentEvents(t.Context(), "user_1", "session_1", 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "evt_1", events[0].RecordID)
}

func TestE2EEEnvelopeAndCursorValidation(t *testing.T) {
	t.Parallel()
	valid := e2eeEnvelope{
		ProtocolVersion: 1, CipherVersion: 1, KeyEpoch: 1, RecordID: "record_1",
		AgentID: "agent_1", SessionID: "session_1", Kind: "acp_event",
		Nonce:   base64.StdEncoding.EncodeToString([]byte("123456789012")),
		Payload: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
	}
	_, err := decodeE2EERecord(valid)
	require.NoError(t, err)

	invalid := valid
	invalid.ProtocolVersion = 2
	_, err = decodeE2EERecord(invalid)
	require.ErrorContains(t, err, "version")
	invalid = valid
	invalid.Nonce = "AA=="
	_, err = decodeE2EERecord(invalid)
	require.ErrorContains(t, err, "nonce")
	invalid = valid
	invalid.RecordID = strings.Repeat("x", 513)
	_, err = decodeE2EERecord(invalid)
	require.ErrorContains(t, err, "metadata")

	req := httptest.NewRequest(http.MethodGet, "/events?after_cursor=42", nil)
	cursor, err := e2eeEventCursor(req)
	require.NoError(t, err)
	assert.Equal(t, int64(42), cursor)
	req.Header.Set("Last-Event-ID", "bad")
	_, err = e2eeEventCursor(req)
	require.Error(t, err)

	assert.Equal(t, "pending", commandStatus(domain.AgentCommand{}))
	now := time.Now()
	assert.Equal(t, "delivered", commandStatus(domain.AgentCommand{DeliveredAt: &now}))
	assert.Equal(t, "acknowledged", commandStatus(domain.AgentCommand{AcknowledgedAt: &now}))
	assert.Empty(t, errorString(nil))
}

func mustE2EEEnvelopeJSON(t *testing.T, envelope e2eeEnvelope) []byte {
	t.Helper()
	data, err := json.Marshal(envelope)
	require.NoError(t, err)
	return data
}

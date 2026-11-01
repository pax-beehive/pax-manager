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

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryAgentGivenOwnedAgentThenRunsRealACPAndRecoversCompletedOutput(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
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
		OwnerID: owner,
		AgentID: fixture.agentID,
		RunID:   "run-1",
		Prompt:  "Summarize the supplied source messages only.",
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
	writeMockACPResponse(
		t,
		ws,
		created.QueueID,
		1,
		createRequest.ID,
		json.RawMessage(`{"sessionId":"secretary-native"}`),
	)
	promptEnvelope, prompt := readMockACPRequest(t, ws, "session/prompt")
	sessionID := secretary.ExecutionSessionID(owner, request.RunID)
	assert.Equal(t, sessionID, findStringFromRaw(prompt.Params, "sessionId"))
	assert.Contains(t, acpPromptText(prompt.Params), "supplied source")
	assert.Equal(
		t,
		secretary.ExecutionTurnID(owner, request.RunID),
		promptEnvelope.Metadata["turn_id"],
	)
	writeMockACPChunk(
		t,
		ws,
		promptEnvelope.QueueID,
		2,
		sessionID,
		`{"title":"Decision","body":"Use the database."}`,
	)
	writeMockACPResponse(
		t,
		ws,
		promptEnvelope.QueueID,
		3,
		prompt.ID,
		json.RawMessage(`{"stopReason":"end_turn"}`),
	)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, sessionID, got.result.SessionID)
		assert.JSONEq(t, `{"title":"Decision","body":"Use the database."}`, got.result.Text)
	case <-time.After(5 * time.Second):
		t.Fatal("Secretary execution did not finish")
	}
	recovered, err := srv.executeSecretaryAgent(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, sessionID, recovered.SessionID)
	assert.Contains(t, recovered.Text, "Use the database")
}

func TestSecretaryAgentGivenInvalidOrForeignRequestThenDoesNotDispatch(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	for _, request := range []secretary.ExecutionRequest{
		{},
		{OwnerID: owner, AgentID: fixture.agentID, RunID: "run"},
		{OwnerID: "missing", AgentID: fixture.agentID, RunID: "run", Prompt: "text"},
		{OwnerID: owner, AgentID: "missing", RunID: "run", Prompt: "text"},
	} {
		_, err := srv.executeSecretaryAgent(t.Context(), request)
		require.Error(t, err)
	}
}

func TestSecretaryAgentGivenExistingIncompleteTurnThenNeverDuplicatesPrompt(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	request := secretary.ExecutionRequest{
		OwnerID: owner,
		AgentID: fixture.agentID,
		RunID:   "run",
		Prompt:  "text",
	}
	sessionID := secretary.ExecutionSessionID(owner, request.RunID)
	createConversationTestSession(t, srv, fixture, sessionID, "native")
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &domain.Message{
		MessageID: "secretary-pending", AgentID: fixture.agentID, SessionID: sessionID,
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent,
		TurnID: secretary.ExecutionTurnID(owner, request.RunID), Role: "user", MessageType: "chat",
	}))
	_, err := srv.executeSecretaryAgent(t.Context(), request)
	assert.ErrorIs(t, err, secretary.ErrExecutionIncomplete)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = srv.executeSecretaryAgent(ctx, request)
	assert.Error(t, err)
}

func TestSecretaryAgentGivenDisabledRunThenSendsNativeCancel(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	request := secretary.ExecutionRequest{
		OwnerID: owner,
		AgentID: fixture.agentID,
		RunID:   "cancel-run",
		Prompt:  "Summarize only.",
	}
	sessionID := secretary.ExecutionSessionID(owner, request.RunID)
	createConversationTestSession(t, srv, fixture, sessionID, "native-cancel")
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, runErr := srv.executeSecretaryAgent(ctx, request); done <- runErr }()
	_, prompt := readMockACPRequest(t, ws, "session/prompt")
	assert.Equal(t, sessionID, findStringFromRaw(prompt.Params, "sessionId"))
	cancel()
	_, cancellation := readMockACPRequest(t, ws, "session/cancel")
	assert.Equal(t, sessionID, findStringFromRaw(cancellation.Params, "sessionId"))
	select {
	case err = <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Secretary task did not stop")
	}
}

func TestSecretaryOutputGivenReasoningThenOnlyReturnsCanonicalVisibleText(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	for _, message := range []domain.Message{
		{MessageID: "old", Role: "assistant", TurnID: "other", MessageType: "chat"},
		{MessageID: "thought", Role: "assistant", TurnID: "turn", MessageType: "agent_thought_chunk"},
		{MessageID: "visible", Role: "assistant", TurnID: "turn", MessageType: "agent_message_chunk"},
		{MessageID: "done", Role: "assistant", TurnID: "turn", MessageType: "turn_done", Status: "complete"},
	} {
		message.AgentID, message.SessionID = fixture.agentID, "session"
		message.Source, message.Direction = domain.MessageSourceACPTunnel, domain.MessageDirectionAgentToUser
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
		require.NoError(
			t,
			srv.store.UpsertMessagePart(
				t.Context(),
				&domain.MessagePart{
					MessageID: message.MessageID,
					PartIndex: 0,
					PartType:  domain.MessagePartText,
					Text:      message.MessageID,
				},
			),
		)
	}
	text, started, completed, err := srv.readSecretaryExecution(
		t.Context(),
		fixture.agentID,
		"session",
		"turn",
	)
	require.NoError(t, err)
	assert.True(t, started)
	assert.True(t, completed)
	assert.Equal(t, "visible", text)
	_, _, _, err = srv.readSecretaryExecution(t.Context(), fixture.agentID, "session", "missing")
	require.NoError(t, err)
}

func TestSecretaryOutputGivenCommentaryAndToolCallsThenReturnsFinalMultipartResponse(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	request := secretary.ExecutionRequest{
		OwnerID: owner, AgentID: fixture.agentID, RunID: "tool-response", Prompt: "Summarize.",
		BeforePrompt: func(context.Context, string, string) error {
			t.Fatal("a completed turn must be recovered without another prompt")
			return domain.ErrConflict
		},
	}
	sessionID := secretary.ExecutionSessionID(owner, request.RunID)
	turnID := secretary.ExecutionTurnID(owner, request.RunID)
	createConversationTestSession(t, srv, fixture, sessionID, "tool-native")
	final := `{"title":"Decision","body":"Use the database."}`
	for index, item := range []struct {
		id, messageType, status string
		otherTurn               bool
		parts                   []string
	}{
		{id: "commentary", messageType: "agent_message_chunk", parts: []string{"I will search for related decisions."}},
		{id: "tool", messageType: "tool_call", parts: []string{`{"body":"Tool output must not become the summary."}`}},
		{id: "final", messageType: "agent_message_chunk", parts: []string{`{"title":"Decision",`, `"body":"Use the database."}`}},
		{id: "trailing-thought", messageType: "agent_thought_chunk", parts: []string{"Private reasoning."}},
		{id: "trailing-tool", messageType: "tool_call_update", parts: []string{`{"body":"Trailing tool status."}`}},
		{id: "done", messageType: "turn_done", status: "complete"},
		{id: "late-notification", messageType: "agent_message_chunk", parts: []string{"A background task reported after this turn completed."}},
		{id: "other-turn", otherTurn: true, messageType: "agent_message_chunk", parts: []string{`{"body":"Another turn."}`}},
		{id: "duplicate-done", messageType: "turn_done", status: "complete"},
	} {
		message := domain.Message{
			MessageID: item.id, AgentID: fixture.agentID, SessionID: sessionID, SessionSeq: int64(index + 1),
			Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionAgentToUser,
			Role: "assistant", TurnID: turnID, MessageType: item.messageType, Status: item.status,
		}
		if item.otherTurn {
			message.TurnID = "other"
		}
		require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
		// Reverse insertion order to exercise canonical part ordering in the final response.
		for partIndex := len(item.parts) - 1; partIndex >= 0; partIndex-- {
			require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
				MessageID: item.id, PartIndex: partIndex, PartType: domain.MessagePartText, Text: item.parts[partIndex],
			}))
		}
	}

	text, started, completed, err := srv.readSecretaryExecution(
		t.Context(),
		fixture.agentID,
		sessionID,
		turnID,
	)
	require.NoError(t, err)
	assert.True(t, started)
	assert.True(t, completed)
	assert.Equal(t, final, text)
	_, err = secretary.RequestedRelatedSources(text)
	assert.NoError(t, err, "the final response must remain valid summary JSON")
	recovered, err := srv.executeSecretaryAgent(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, final, recovered.Text)
}

func TestSecretaryAgentGivenFencedRegistrationThenNeverPromptsAgent(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	request := secretary.ExecutionRequest{
		OwnerID: owner, AgentID: fixture.agentID, RunID: "fenced-run", Prompt: "Summarize.",
		BeforePrompt: func(_ context.Context, agentID, sessionID string) error {
			assert.Equal(t, fixture.agentID, agentID)
			assert.Equal(t, secretary.ExecutionSessionID(owner, "fenced-run"), sessionID)
			return domain.ErrSecretaryFenced
		},
	}
	_, err := srv.executeSecretaryAgent(t.Context(), request)
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	request.BeforePrompt = nil
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err = srv.executeSecretaryAgent(ctx, request)
	assert.Error(t, err)
}

func TestSecretaryAgentGivenArchivedInternalSessionThenDoesNotRun(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	request := secretary.ExecutionRequest{
		OwnerID: principal.User.UserID,
		AgentID: fixture.agentID,
		RunID:   "archived-run",
		Prompt:  "Summarize.",
	}
	sessionID := secretary.ExecutionSessionID(request.OwnerID, request.RunID)
	createConversationTestSession(t, srv, fixture, sessionID, "native-archived")
	archived := true
	_, err := srv.store.UpdateNodeAgentSession(t.Context(), principal, domain.UpdateSessionRequest{
		NodeID: fixture.nodeID, AgentID: fixture.agentID, SessionID: sessionID, Archived: &archived,
	})
	require.NoError(t, err)
	_, err = srv.executeSecretaryAgent(t.Context(), request)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSecretaryOutputGivenCompletedEmptyTurnThenRejectsSummaryInput(t *testing.T) {
	srv, _ := testServer(t, "todd@example.com")
	fixture := testNodeAgent(t, srv, "todd@example.com")
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &domain.Message{
		MessageID: "empty-done", AgentID: fixture.agentID, SessionID: "empty-session", Source: domain.MessageSourceACPTunnel,
		Direction: domain.MessageDirectionAgentToUser, Role: "assistant", TurnID: "empty-turn", MessageType: "turn_done", Status: "complete",
	}))
	_, started, completed, err := srv.readSecretaryExecution(
		t.Context(),
		fixture.agentID,
		"empty-session",
		"empty-turn",
	)
	assert.True(t, started)
	assert.True(t, completed)
	assert.ErrorIs(t, err, secretary.ErrNoOutput)
}

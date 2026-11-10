package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pax-beehive/paxkit/reliablemq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryExecutionGivenReliableCancelledPromptWithoutWaiterThenRecoversKnownFailure(
	t *testing.T,
) {
	srv, _ := testServer(t, "terminal@example.test")
	fixture := testNodeAgent(t, srv, "terminal@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	sessionID := secretary.ExecutionSessionID(owner, "attempt")
	turnID := secretary.ExecutionTurnID(owner, "attempt")
	createConversationTestSession(t, srv, fixture, sessionID, "native-terminal")
	agent := &ACPTunnelAgent{agentID: fixture.agentID, ownerUserID: owner,
		nodeID: fixture.nodeID, connectionID: "terminal-queue", store: srv.store}
	// This record is prepared before dispatch and contains no prompt text.
	marker := domain.Message{
		MessageID: acpHistoryMessageID(
			"secretary:request:" + owner + ":" + fixture.agentID + ":" + sessionID + ":" + turnID,
		),
		OwnerUserID: owner,
		AgentID:     fixture.agentID,
		SessionID:   sessionID,
		TurnID:      turnID,
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		Role:        "system",
		Status:      "prepared",
		MessageType: "secretary_execution_request",
		RawJSON:     json.RawMessage(`{"version":1,"method":"session/prompt","request_id":41}`),
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &marker))
	frame := reliablemq.Frame{
		Key: reliablemq.FrameKey{QueueID: "terminal-queue", Stream: reliablemq.StreamACP,
			Direction: reliablemq.DirectionInbound, Seq: 1},
		Kind:    reliablemq.FrameKindData,
		Payload: json.RawMessage(`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"cancelled"}}`),
		Metadata: reliablemq.Metadata{
			"agent_id":           fixture.agentID,
			"manager_session_id": sessionID,
			"turn_id":            turnID,
		},
		CreatedAt: time.Now(),
	}
	require.NoError(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), frame),
	)
	_, _, completed, err := srv.readSecretaryExecution(
		t.Context(),
		fixture.agentID,
		sessionID,
		turnID,
	)
	assert.False(t, completed)
	require.ErrorContains(t, err, "secretary execution ended without success")
	// Replaying on a fresh runtime must neither require a waiter nor duplicate the terminal.
	srv.acpRuntime = newACPRuntimeProjector(time.Now)
	require.NoError(
		t,
		agent.dispatchReliableACPFrame(t.Context(), srv.agentACPFramePipeline(), frame),
	)
	messages, err := srv.store.ListMessages(t.Context(), fixture.agentID, sessionID, 1000)
	require.NoError(t, err)
	count := 0
	for _, message := range messages {
		if message.MessageType == "secretary_execution_failed" {
			count++
			assert.NotContains(t, string(message.RawJSON), "prompt text")
		}
		assert.NotEqual(t, "turn_done", message.MessageType)
	}
	assert.Equal(t, 1, count)
}

type secretaryTerminalFixture struct {
	service   *Service
	agent     *ACPTunnelAgent
	job       domain.SecretaryJob
	sessionID string
	turnID    string
	frame     reliablemq.Frame
}

func newSecretaryTerminalFixture(t *testing.T) secretaryTerminalFixture {
	t.Helper()
	srv, _ := testServer(t, "terminal-fixture@example.test")
	fixture := testNodeAgent(t, srv, "terminal-fixture@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	job := domain.SecretaryJob{OwnerUserID: owner, AgentID: fixture.agentID, SourceID: "source",
		BatchID: "batch", AttemptID: "attempt", RunID: "attempt", SettingsRevision: 2, RetryGeneration: 3, RetryEpoch: 4,
		Attempt: &domain.SecretaryAttempt{ID: "attempt", BatchID: "batch", State: "unknown"}}
	sessionID, turnID := secretary.ExecutionSessionID(
		owner,
		job.RunID,
	), secretary.ExecutionTurnID(
		owner,
		job.RunID,
	)
	createConversationTestSession(t, srv, fixture, sessionID, "native-terminal")
	agent := &ACPTunnelAgent{agentID: fixture.agentID, ownerUserID: owner,
		nodeID: fixture.nodeID, connectionID: "terminal-queue", store: srv.store}
	require.NoError(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			agent,
			sessionID,
			turnID,
			json.RawMessage("41"),
			&job,
		),
	)
	return secretaryTerminalFixture{
		service:   srv,
		agent:     agent,
		job:       job,
		sessionID: sessionID,
		turnID:    turnID,
		frame: reliablemq.Frame{
			Key: reliablemq.FrameKey{QueueID: "terminal-queue", Stream: reliablemq.StreamACP,
				Direction: reliablemq.DirectionInbound, Seq: 1},
			Kind: reliablemq.FrameKindData,
			Payload: json.RawMessage(
				`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"cancelled"}}`,
			),
			Metadata: reliablemq.Metadata{
				"agent_id":           fixture.agentID,
				"manager_session_id": sessionID,
				"turn_id":            turnID,
			},
			CreatedAt: time.Now(),
		},
	}
}

func TestSecretaryTerminalGivenUnsealedOrUnrelatedRPCThenRemainsUnknown(t *testing.T) {
	f := newSecretaryTerminalFixture(t)
	mutations := map[string]func(*reliablemq.Frame){
		"missing turn": func(frame *reliablemq.Frame) { delete(frame.Metadata, "turn_id") },
		"other turn": func(frame *reliablemq.Frame) {
			frame.Metadata["turn_id"] = secretary.ExecutionTurnID(f.job.OwnerUserID, "other")
		},
		"missing session": func(frame *reliablemq.Frame) { delete(frame.Metadata, "manager_session_id") },
		"foreign agent":   func(frame *reliablemq.Frame) { frame.Metadata["agent_id"] = "other" },
		"other queue":     func(frame *reliablemq.Frame) { frame.Key.QueueID = "other" },
		"outbound":        func(frame *reliablemq.Frame) { frame.Key.Direction = reliablemq.DirectionOutbound },
		"unrelated request": func(frame *reliablemq.Frame) {
			frame.Payload = json.RawMessage(
				`{"jsonrpc":"2.0","id":42,"error":{"code":-1,"message":"tool failed"}}`,
			)
		},
		"string id alias": func(frame *reliablemq.Frame) {
			frame.Payload = json.RawMessage(
				`{"jsonrpc":"2.0","id":"41","result":{"stopReason":"cancelled"}}`,
			)
		},
		"worker request": func(frame *reliablemq.Frame) {
			frame.Payload = json.RawMessage(
				`{"jsonrpc":"2.0","id":41,"method":"session/request_permission","error":{"code":-1,"message":"failed"}}`,
			)
		},
		"malformed error": func(frame *reliablemq.Frame) { frame.Payload = json.RawMessage(`{"jsonrpc":"2.0","id":41,"error":{}}`) },
		"conflicting result": func(frame *reliablemq.Frame) {
			frame.Payload = json.RawMessage(
				`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"cancelled"},"error":{"code":-1,"message":"failed"}}`,
			)
		},
		"end turn": func(frame *reliablemq.Frame) {
			frame.Payload = json.RawMessage(
				`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"end_turn"}}`,
			)
		},
		"unknown stop": func(frame *reliablemq.Frame) {
			frame.Payload = json.RawMessage(
				`{"jsonrpc":"2.0","id":41,"result":{"stopReason":"OTHER"}}`,
			)
		},
		"invalid json": func(frame *reliablemq.Frame) { frame.Payload = json.RawMessage(`{`) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			frame := f.frame.Clone()
			mutate(&frame)
			require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, frame))
			_, started, completed, err := f.service.readSecretaryExecution(
				t.Context(),
				f.agent.agentID,
				f.sessionID,
				f.turnID,
			)
			require.NoError(t, err)
			assert.False(t, started, "prepared dispatch identity is not proof of execution")
			assert.False(t, completed)
		})
	}
}

func TestSecretaryTerminalGivenRPCErrorAndStorageRetryThenRecoversWithoutRawError(t *testing.T) {
	f := newSecretaryTerminalFixture(t)
	failing := &secretaryTerminalFailingStore{Store: f.service.store, fail: true}
	f.agent.store = failing
	f.frame.Payload = json.RawMessage(
		`{"jsonrpc":"2.0","id":41,"error":{"code":-32603,"message":"private failure text"}}`,
	)
	assert.ErrorIs(
		t,
		persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame),
		domain.ErrConflict,
	)
	failing.fail = false
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	request := secretary.ExecutionRequest{
		OwnerID: f.job.OwnerUserID,
		AgentID: f.job.AgentID,
		RunID:   f.job.RunID,
		Job:     f.job,
	}
	result, completed, err := f.service.recoverSecretaryAgent(t.Context(), request)
	assert.ErrorIs(t, err, domain.ErrSecretaryExecutionFailed)
	assert.False(t, completed)
	assert.Empty(t, result.Text)
	messages, err := f.service.store.ListMessages(t.Context(), f.agent.agentID, f.sessionID, 1000)
	require.NoError(t, err)
	for _, message := range messages {
		assert.NotContains(t, string(message.RawJSON), "private failure text")
	}
}

func TestSecretaryTerminalGivenRouteRetryThenOnlyCurrentRequestFailureApplies(t *testing.T) {
	f := newSecretaryTerminalFixture(t)
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	require.NoError(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			f.agent,
			f.sessionID,
			f.turnID,
			json.RawMessage("42"),
			&f.job,
		),
	)
	_, _, _, err := f.service.readSecretaryExecution(
		t.Context(),
		f.agent.agentID,
		f.sessionID,
		f.turnID,
	)
	require.NoError(t, err, "the old request terminal must not hide the replacement request")
	// A delayed copy of the old terminal cannot become the replacement terminal.
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	output := domain.Message{
		MessageID:   "final-output",
		AgentID:     f.agent.agentID,
		SessionID:   f.sessionID,
		TurnID:      f.turnID,
		Role:        "assistant",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		MessageType: "agent_message_chunk",
	}
	require.NoError(t, f.service.store.UpsertMessage(t.Context(), &output))
	require.NoError(
		t,
		f.service.store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: output.MessageID,
				PartType:  domain.MessagePartText,
				Text:      "final output",
			},
		),
	)
	require.NoError(t, persistConversationTurnDone(t.Context(), f.agent, f.sessionID, f.turnID))
	text, _, completed, err := f.service.readSecretaryExecution(
		t.Context(),
		f.agent.agentID,
		f.sessionID,
		f.turnID,
	)
	require.NoError(t, err)
	assert.True(t, completed)
	assert.Equal(t, "final output", text)
}

func TestSecretaryTerminalGivenLongToolHistoryThenStillFindsPreparedCorrelation(t *testing.T) {
	f := newSecretaryTerminalFixture(t)
	for index := range 1001 {
		message := domain.Message{
			MessageID:   fmt.Sprintf("tool-%d", index),
			AgentID:     f.agent.agentID,
			SessionID:   f.sessionID,
			Source:      domain.MessageSourceACPTunnel,
			Direction:   domain.MessageDirectionAgentToUser,
			MessageType: "tool_call",
		}
		require.NoError(t, f.service.store.UpsertMessage(t.Context(), &message))
	}
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	_, _, completed, err := f.service.readSecretaryExecution(
		t.Context(),
		f.agent.agentID,
		f.sessionID,
		f.turnID,
	)
	assert.ErrorIs(t, err, domain.ErrSecretaryExecutionFailed)
	assert.False(t, completed)
}

func TestSecretaryExecutionGivenTimeoutThenReliableCancellationAfterWaiterRemovalRecoversFailure(
	t *testing.T,
) {
	srv, _ := testServer(t, "terminal-timeout@example.test")
	fixture := testNodeAgent(t, srv, "terminal-timeout@example.test")
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
		RunID:   "timeout-attempt",
		Prompt:  "Synthetic test prompt.",
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, runErr := srv.executeSecretaryAgent(ctx, request); done <- runErr }()
	created, newRequest := readMockACPRequest(t, ws, "session/new")
	writeMockACPResponse(
		t,
		ws,
		created.QueueID,
		1,
		newRequest.ID,
		json.RawMessage(`{"sessionId":"timeout-native"}`),
	)
	promptEnvelope, prompt := readMockACPRequest(t, ws, "session/prompt")
	_, _ = readMockACPRequest(t, ws, "session/cancel")
	select {
	case runErr := <-done:
		assert.ErrorIs(t, runErr, context.DeadlineExceeded)
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled execution did not return")
	}
	// The worker has returned, so its response waiter and SSE subscription are gone.
	terminal, err := json.Marshal(
		map[string]any{
			"jsonrpc": "2.0",
			"id":      prompt.ID,
			"result":  map[string]string{"stopReason": "cancelled"},
		},
	)
	require.NoError(t, err)
	data, err := reliablemq.MarshalEnvelope(reliablemq.Envelope{Type: reliablemq.EnvelopeTypeData,
		QueueID: promptEnvelope.QueueID, Stream: reliablemq.StreamACP, Seq: 2, Payload: terminal,
		Metadata: reliablemq.Metadata{"agent_id": fixture.agentID,
			"manager_session_id": secretary.ExecutionSessionID(
				owner,
				request.RunID,
			), "turn_id": secretary.ExecutionTurnID(owner, request.RunID)}})
	require.NoError(t, err)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, data))
	require.Eventually(t, func() bool {
		_, _, completed, readErr := srv.readSecretaryExecution(
			t.Context(),
			fixture.agentID,
			secretary.ExecutionSessionID(
				owner,
				request.RunID,
			),
			secretary.ExecutionTurnID(owner, request.RunID),
		)
		return !completed && errors.Is(readErr, domain.ErrSecretaryExecutionFailed)
	}, 3*time.Second, 10*time.Millisecond)
}

type secretaryTerminalFailingStore struct {
	Store
	fail        bool
	failRequest bool
	failSession bool
	failHistory bool
}

func (s *secretaryTerminalFailingStore) UpsertMessage(
	ctx context.Context,
	message *domain.Message,
) error {
	if s.fail && message.MessageType == "secretary_execution_failed" {
		return domain.ErrConflict
	}
	if s.failRequest && message.MessageType == "secretary_execution_request" {
		return domain.ErrConflict
	}
	return s.Store.UpsertMessage(ctx, message)
}

func (s *secretaryTerminalFailingStore) GetSession(
	ctx context.Context,
	principal domain.UserPrincipal,
	sessionID string,
) (domain.AgentSession, error) {
	if s.failSession {
		return domain.AgentSession{}, domain.ErrConflict
	}
	return s.Store.GetSession(ctx, principal, sessionID)
}

func (s *secretaryTerminalFailingStore) ListMessageHistoryPage(
	ctx context.Context,
	agentID, sessionID string,
	before int64,
	limit int,
) (domain.MessageHistoryPage, error) {
	if s.failHistory {
		return domain.MessageHistoryPage{}, domain.ErrConflict
	}
	return s.Store.ListMessageHistoryPage(ctx, agentID, sessionID, before, limit)
}

func TestSecretaryCorrelationGivenInvalidIdentityOrStorageFailureThenDoesNotClaimAResult(
	t *testing.T,
) {
	f := newSecretaryTerminalFixture(t)
	require.NoError(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			nil,
			"ordinary-session",
			"ordinary-turn",
			nil,
			nil,
		),
	)
	assert.ErrorIs(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			nil,
			f.sessionID,
			f.turnID,
			json.RawMessage("41"),
			nil,
		),
		domain.ErrConflict,
	)
	bad := f.job
	bad.AttemptID, bad.RunID = "other-attempt", "other-attempt"
	assert.ErrorIs(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			f.agent,
			f.sessionID,
			f.turnID,
			json.RawMessage("41"),
			&bad,
		),
		domain.ErrSecretaryFenced,
	)
	store := &secretaryTerminalFailingStore{Store: f.service.store, failRequest: true}
	f.agent.store = store
	assert.ErrorIs(
		t,
		persistSecretaryPromptCorrelation(
			t.Context(),
			f.agent,
			f.sessionID,
			f.turnID,
			json.RawMessage("41"),
			nil,
		),
		domain.ErrConflict,
	)
	store.failSession = true
	assert.ErrorIs(
		t,
		persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame),
		domain.ErrConflict,
	)
	store.failSession, store.failHistory = false, true
	assert.ErrorIs(
		t,
		persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame),
		domain.ErrConflict,
	)
	store.failHistory = false
	correlationID := domain.SecretaryExecutionRequestMessageID(f.job)
	marker := domain.Message{
		MessageID:   correlationID,
		OwnerUserID: f.job.OwnerUserID,
		AgentID:     f.job.AgentID,
		SessionID:   f.sessionID,
		TurnID:      f.turnID,
		MessageType: secretaryExecutionRequestType,
		Status:      "prepared",
		Role:        "system",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		RawJSON:     json.RawMessage(`{`),
	}
	require.NoError(t, f.service.store.UpsertMessage(t.Context(), &marker))
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	_, _, _, err := f.service.readSecretaryExecution(
		t.Context(),
		f.agent.agentID,
		f.sessionID,
		f.turnID,
	)
	require.NoError(t, err)
}

type secretaryTerminalWakeStore struct {
	Store
	fail bool
	jobs []domain.SecretaryJob
}

func (s *secretaryTerminalWakeStore) WakeSecretaryExecutionRecovery(
	_ context.Context,
	job domain.SecretaryJob,
) error {
	s.jobs = append(s.jobs, job)
	if s.fail {
		return domain.ErrConflict
	}
	return nil
}

func TestSecretaryTerminalGivenWakeFailureThenReliableRetryRetainsOneTerminalAndTrustedBinding(
	t *testing.T,
) {
	f := newSecretaryTerminalFixture(t)
	store := &secretaryTerminalWakeStore{Store: f.service.store, fail: true}
	f.agent.store = store
	assert.ErrorIs(
		t,
		persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame),
		domain.ErrConflict,
	)
	store.fail = false
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	require.Len(t, store.jobs, 2)
	for _, job := range store.jobs {
		assert.Equal(t, f.job.SourceID, job.SourceID)
		assert.Equal(t, f.job.AttemptID, job.AttemptID)
		assert.Equal(t, f.job.SettingsRevision, job.SettingsRevision)
		assert.Equal(t, f.job.RetryGeneration, job.RetryGeneration)
		assert.Equal(t, f.job.RetryEpoch, job.RetryEpoch)
		assert.Empty(t, job.LeaseToken)
	}
	messages, err := f.service.store.ListMessages(t.Context(), f.agent.agentID, f.sessionID, 1000)
	require.NoError(t, err)
	count := 0
	for _, message := range messages {
		if message.MessageType == secretaryExecutionFailedType {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestSecretaryTerminalGivenTamperedFailureBindingThenDoesNotRecoverFailure(t *testing.T) {
	f := newSecretaryTerminalFixture(t)
	require.NoError(t, persistSecretaryExecutionTerminal(t.Context(), f.agent, f.frame))
	messages, err := f.service.store.ListMessages(t.Context(), f.agent.agentID, f.sessionID, 1000)
	require.NoError(t, err)
	var failed domain.Message
	for _, message := range messages {
		if message.MessageType == secretaryExecutionFailedType {
			failed = message
		}
	}
	require.NotEmpty(t, failed.MessageID)
	var terminal secretaryExecutionCorrelation
	require.NoError(t, json.Unmarshal(failed.RawJSON, &terminal))
	terminal.Binding.AttemptID = "other"
	failed.RawJSON, err = json.Marshal(terminal)
	require.NoError(t, err)
	require.NoError(t, f.service.store.UpsertMessage(t.Context(), &failed))
	_, _, _, err = f.service.readSecretaryExecution(
		t.Context(),
		f.agent.agentID,
		f.sessionID,
		f.turnID,
	)
	require.NoError(t, err)
	failed.RawJSON = json.RawMessage(`{`)
	require.NoError(t, f.service.store.UpsertMessage(t.Context(), &failed))
	_, _, _, err = f.service.readSecretaryExecution(
		t.Context(),
		f.agent.agentID,
		f.sessionID,
		f.turnID,
	)
	require.NoError(t, err)
	for raw, valid := range map[string]bool{`41`: true, `"request"`: true, ``: false, `null`: false, `-1`: false, `{}`: false} {
		assert.Equal(t, valid, validSecretaryPromptRequestID(json.RawMessage(raw)), raw)
	}
}

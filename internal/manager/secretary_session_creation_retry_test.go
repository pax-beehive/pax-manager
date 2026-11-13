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

func TestSecretaryAgentGivenNoReadySlotDuringSessionCreationThenBacksOffWithoutDispatchingPrompt(
	t *testing.T,
) {
	srv, fixture, job := secretarySlotRetryFixture(t)
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	done := make(chan error, 1)
	go func() {
		_, err := srv.executeSecretaryAgent(t.Context(), secretarySlotRetryRequest(srv, job))
		done <- err
	}()
	frame, request := readMockACPRequest(t, ws, "session/new")
	writeMockACPError(t, ws, frame.QueueID, 1, request.ID, -32003,
		"no ready ACP slot", "slot_unavailable", true)
	var failure error
	select {
	case failure = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Secretary did not finish the rejected session creation")
	}
	require.ErrorIs(t, failure, domain.ErrSecretaryNotDispatched)
	assert.NotErrorIs(t, failure, domain.ErrSecretaryExecutionUnknown)
	assert.True(t, isConversationACPErrorKind(failure, "slot_unavailable"))
	messages, err := srv.store.ListMessages(t.Context(), job.AgentID,
		secretary.ExecutionSessionID(job.OwnerUserID, job.AttemptID), 100)
	require.NoError(t, err)
	for _, message := range messages {
		assert.NotEqual(t, secretaryExecutionRequestType, message.MessageType,
			"a rejected session/new must not prepare session/prompt")
	}
	executions := srv.secretaryStore.(domain.SecretaryExecutionStore)
	require.NoError(t, executions.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure))
	detail, err := srv.secretaryStore.(domain.SecretaryTaskStore).GetSecretaryTask(
		t.Context(), job.OwnerUserID, job.SourceID, domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	assert.Equal(t, "waiting_retry", detail.Task.ExecutionState)
	assert.Equal(t, 1, detail.Task.ConnectionFailures)
	require.NotNil(t, detail.Task.NextRetryAt)
	assert.Equal(t, srv.clock().Add(domain.SecretaryConnectionRetryDelay(1, job.AttemptID)),
		*detail.Task.NextRetryAt)
	require.Len(t, detail.Attempts, 1)
	assert.True(t, detail.Attempts[0].NotDispatched)
	jobs, err := srv.secretaryStore.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	assert.Empty(t, jobs, "the existing bounded connection deadline must prevent immediate retry")
}

func TestSecretaryAgentGivenCancellationAfterPromptDispatchThenDoesNotMarkUndispatched(
	t *testing.T,
) {
	srv, fixture, job := secretarySlotRetryFixture(t)
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := srv.executeSecretaryAgent(ctx, secretarySlotRetryRequest(srv, job))
		done <- err
	}()
	frame, request := readMockACPRequest(t, ws, "session/new")
	writeMockACPResponse(t, ws, frame.QueueID, 1, request.ID,
		json.RawMessage(`{"sessionId":"slot-retry-native"}`))
	_, _ = readMockACPRequest(t, ws, "session/prompt")
	cancel()
	select {
	case failure := <-done:
		assert.ErrorIs(t, failure, context.Canceled)
		assert.NotErrorIs(t, failure, domain.ErrSecretaryNotDispatched)
	case <-time.After(5 * time.Second):
		t.Fatal("Secretary did not finish the canceled prompt")
	}
}

func TestSecretaryAgentGivenAnySessionCreationFailureThenRetriesInsteadOfBecomingUnknown(
	t *testing.T,
) {
	srv, fixture, job := secretarySlotRetryFixture(t)
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	done := make(chan error, 1)
	go func() {
		_, err := srv.executeSecretaryAgent(t.Context(), secretarySlotRetryRequest(srv, job))
		done <- err
	}()
	frame, request := readMockACPRequest(t, ws, "session/new")
	writeMockACPError(t, ws, frame.QueueID, 1, request.ID, -32603,
		"adapter failed to start", "", false)
	var failure error
	select {
	case failure = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Secretary did not finish the rejected session creation")
	}
	// session/new precedes session/prompt: no native turn can exist.
	require.ErrorIs(t, failure, domain.ErrSecretaryNotDispatched)
	assert.NotErrorIs(t, failure, domain.ErrSecretaryExecutionUnknown)
	executions := srv.secretaryStore.(domain.SecretaryExecutionStore)
	require.NoError(t, executions.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure))
	detail, err := srv.secretaryStore.(domain.SecretaryTaskStore).GetSecretaryTask(
		t.Context(), job.OwnerUserID, job.SourceID, domain.SecretaryTaskQuery{})
	require.NoError(t, err)
	assert.Equal(t, "waiting_retry", detail.Task.ExecutionState)
	require.Len(t, detail.Attempts, 1)
	assert.True(t, detail.Attempts[0].NotDispatched)
}

func TestSecretaryAgentGivenPromptFailsAfterDispatchThenStopsTheNativeTurn(t *testing.T) {
	srv, fixture, job := secretarySlotRetryFixture(t)
	ws := secretarySlotRetryTunnel(t, srv, fixture)
	done := make(chan error, 1)
	go func() {
		_, err := srv.executeSecretaryAgent(t.Context(), secretarySlotRetryRequest(srv, job))
		done <- err
	}()
	frame, request := readMockACPRequest(t, ws, "session/new")
	writeMockACPResponse(t, ws, frame.QueueID, 1, request.ID,
		json.RawMessage(`{"sessionId":"slot-retry-native"}`))
	frame, request = readMockACPRequest(t, ws, "session/prompt")
	writeMockACPError(t, ws, frame.QueueID, 2, request.ID, -32603,
		"model connection failed", "", false)
	// The context is still live: the turn must be stopped for any unfinished
	// dispatched prompt, not only after a timeout.
	_, cancelRequest := readMockACPRequest(t, ws, "session/cancel")
	assert.Equal(t, "session/cancel", cancelRequest.Method)
	select {
	case failure := <-done:
		require.Error(t, failure)
		assert.NotErrorIs(t, failure, domain.ErrSecretaryNotDispatched,
			"a dispatched prompt must never be replayed as undispatched")
	case <-time.After(5 * time.Second):
		t.Fatal("Secretary did not finish the failed prompt")
	}
}

func secretarySlotRetryFixture(
	t *testing.T,
) (*Service, conversationTestFixture, domain.SecretaryJob) {
	t.Helper()
	srv, _ := testServer(t, "slot-retry@example.test")
	fixture := testNodeAgent(t, srv, "slot-retry@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	createConversationTestSession(t, srv, fixture, "slot-retry-source", "slot-retry-source-native")
	message := domain.Message{MessageID: "slot-retry-message", AgentID: fixture.agentID,
		SessionID: "slot-retry-source", Role: "user", Status: "complete", MessageType: "user_message",
		Source: domain.MessageSourceACPTunnel, Direction: domain.MessageDirectionUserToAgent}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	require.NoError(t, srv.store.UpsertMessagePart(t.Context(), &domain.MessagePart{
		MessageID: message.MessageID, PartType: domain.MessagePartText, Text: "A settled request.",
	}))
	_, err := srv.secretaryStore.UpdateSecretarySettings(t.Context(), owner,
		domain.SecretarySettingsUpdate{AgentID: fixture.agentID, Enabled: true})
	require.NoError(t, err)
	jobs, err := srv.secretaryStore.ClaimSecretaryJobs(t.Context(), 1, time.Minute, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	executions := srv.secretaryStore.(domain.SecretaryExecutionStore)
	job, err := executions.PrepareSecretaryBatch(t.Context(), jobs[0])
	require.NoError(t, err)
	attempt, err := executions.BeginSecretaryAttempt(t.Context(), job)
	require.NoError(t, err)
	job.AttemptID, job.RunID, job.Attempt = attempt.ID, attempt.ID, &attempt
	return srv, fixture, job
}

func secretarySlotRetryTunnel(
	t *testing.T,
	srv *Service,
	fixture conversationTestFixture,
) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	ws := dialMockAgentTunnel(t, "ws"+strings.TrimPrefix(server.URL, "http"),
		fixture.nodeAPIKey, fixture.agentID)
	t.Cleanup(func() { _ = ws.Close() })
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")
	return ws
}

func secretarySlotRetryRequest(srv *Service, job domain.SecretaryJob) secretary.ExecutionRequest {
	return secretary.ExecutionRequest{
		OwnerID: job.OwnerUserID, AgentID: job.AgentID, RunID: job.RunID, Job: job,
		Prompt: "Maintain summaries for the supplied messages.",
		BeforePrompt: func(ctx context.Context, agentID, sessionID string) error {
			return srv.secretaryStore.RegisterSecretaryInternalSession(ctx, job, agentID, sessionID)
		},
	}
}

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

type secretaryValidationRetryObservedStore struct {
	domain.SecretaryStore
	domain.SecretaryExecutionStore
	domain.SecretaryNodeStore
	claimed chan domain.SecretaryJob
}

func (s *secretaryValidationRetryObservedStore) BeginSecretaryAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
) (domain.SecretaryAttempt, error) {
	attempt, err := s.SecretaryExecutionStore.BeginSecretaryAttempt(ctx, job)
	if err == nil {
		job.Attempt, job.AttemptID, job.RunID = &attempt, attempt.ID, attempt.ID
		s.claimed <- job
	}
	return attempt, err
}

func (s *secretaryValidationRetryObservedStore) ClaimSecretaryJobs(
	ctx context.Context, limit int, lease, settle time.Duration,
) ([]domain.SecretaryJob, error) {
	jobs, err := s.SecretaryStore.ClaimSecretaryJobs(ctx, limit, lease, settle)
	return jobs, err
}

func TestSecretaryValidationRetryGivenRejectedCompletedACPOutputThenPublishesFreshValidRun(
	t *testing.T,
) {
	for _, code := range []string{"schema", "evidence"} {
		t.Run(code, func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Now().UTC().UnixNano())
			store := storage.NewMemoryStore(
				func() time.Time { return time.Unix(0, clock.Load()).UTC() },
			)
			assertSecretaryValidationRetry(
				t,
				store,
				code,
				func() { clock.Add(int64(6 * time.Second)) },
			)
		})
	}
}

func assertSecretaryValidationRetry(t *testing.T, store Store, code string, releaseBackoff func()) {
	t.Helper()
	srv := newServer(Config{LocalUserID: "local@example.test", AllowLocalUserHeader: true}, store)
	t.Cleanup(func() { require.NoError(t, srv.CloseTransportStore(context.Background())) })
	fixture := testNodeAgent(t, srv, "validation-retry@example.test")
	owner := testUserPrincipal(t, srv, fixture.userEmail).User.UserID
	createConversationTestSession(t, srv, fixture, "source-session", "source-native")
	putSecretaryToolMessage(t, srv, fixture, "retry-source", "user", "Use PostgreSQL.")
	jobs := store.(domain.SecretaryStore)
	_, err := jobs.UpdateSecretarySettings(t.Context(), owner, domain.SecretarySettingsUpdate{
		AgentID: fixture.agentID, Enabled: true,
	})
	require.NoError(t, err)
	observed := &secretaryValidationRetryObservedStore{
		SecretaryStore: jobs, SecretaryExecutionStore: store.(domain.SecretaryExecutionStore), SecretaryNodeStore: store.(domain.SecretaryNodeStore), claimed: make(chan domain.SecretaryJob, 2),
	}
	worker := secretary.NewWorker(observed, srv.secretaryExecutor(), secretary.WorkerConfig{
		SettleDuration: time.Nanosecond, ExecutionTimeout: 10 * time.Second,
		ActiveCheckInterval: 10 * time.Millisecond,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.Handle("/", srv.routes())
	server := httptest.NewServer(mux)
	defer server.Close()
	ws := dialMockAgentTunnel(
		t,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		fixture.nodeAPIKey,
		fixture.agentID,
	)
	defer func() { _ = ws.Close() }()
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")
	runner := secretaryValidationRetryACP{t: t, worker: worker, observed: observed, ws: ws}
	releaseBackoff()
	invalid := "not JSON"
	if code == "evidence" {
		invalid = `{"summaries":[{"action":"create","proposal_key":"database","body":"Unsupported","source_message_ids":["retry-source"],"claims":[{"text":"Unsupported","evidence":[{"message_id":"retry-source","quote":"A fabricated quote."}]}]}],"outcomes":[{"message_id":"retry-source","action":"summarized","summary_keys":["database"]}]}`
	}
	first, firstSession, failure := runner.run(invalid)
	require.Error(t, failure)
	assert.Equal(t, code, domain.SecretaryValidationFailureCode(failure))
	assert.Zero(t, first.RetryGeneration)
	assert.Empty(t, first.ValidationFeedback)
	status, err := jobs.GetSecretaryStatus(t.Context(), owner)
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
	assert.Equal(t, failure.Error(), status.LastError)
	assert.Nil(t, status.LastCompletedAt)
	cached, started, completed, err := srv.readSecretaryExecution(
		t.Context(), fixture.agentID, firstSession, secretary.ExecutionTurnID(owner, first.RunID),
	)
	require.NoError(t, err)
	assert.True(t, started)
	assert.True(t, completed)
	assert.Equal(t, invalid, cached, "the rejected completed response remains recoverable")
	releaseBackoff()
	valid := `{"summaries":[{"action":"create","proposal_key":"database","title":"Database decision","body":"Use PostgreSQL.","source_message_ids":["retry-source"],"claims":[{"text":"Use PostgreSQL.","evidence":[{"message_id":"retry-source","quote":"PostgreSQL"}]}]}],"outcomes":[{"message_id":"retry-source","action":"summarized","summary_keys":["database"]}]}`
	second, secondSession, err := runner.run(valid)
	require.NoError(t, err)
	assert.EqualValues(t, 1, second.RetryGeneration)
	assert.Equal(t, code, second.ValidationFeedback)
	assert.NotEqual(t, first.RunID, second.RunID)
	assert.NotEqual(
		t,
		firstSession,
		secondSession,
		"fresh validation retry does not recover the rejected turn again",
	)
	summary, err := store.(domain.SummaryStore).GetSummary(
		t.Context(),
		owner,
		domain.SecretaryNodeID(second.BatchID, "database"),
		0,
	)
	require.NoError(t, err)
	assert.EqualValues(t, 1, summary.Revision)
	assert.Equal(t, "Use PostgreSQL.", summary.Body)
	require.Len(t, summary.Coverage, 1)
	assert.Equal(t, "retry-source", summary.Coverage[0].MessageID)
	assert.Equal(t, domain.SearchContentHash("Use PostgreSQL."), summary.Coverage[0].ContentHash)
	require.Len(t, summary.Claims, 1)
	assert.Equal(t, summary.Coverage[0], summary.Claims[0].Evidence[0].Message)
	frozen, err := store.(domain.SummaryStore).ReadMessageVersion(
		t.Context(),
		owner,
		summary.Coverage[0],
	)
	require.NoError(t, err)
	assert.Equal(t, "Use PostgreSQL.", frozen.Body)
	status, err = jobs.GetSecretaryStatus(t.Context(), owner)
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
	assert.Empty(t, status.LastError)
	require.NotNil(t, status.LastCompletedAt)
	assert.False(t, status.Processing)
}

type secretaryValidationRetryACP struct {
	t        *testing.T
	worker   *secretary.Worker
	observed *secretaryValidationRetryObservedStore
	ws       *websocket.Conn
	sequence int64
	runs     int
}

func (r *secretaryValidationRetryACP) run(answer string) (domain.SecretaryJob, string, error) {
	t := r.t
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.worker.ProcessOnce(ctx) }()
	var job domain.SecretaryJob
	select {
	case job = <-r.observed.claimed:
	case err := <-done:
		t.Fatalf("worker returned before claiming the source: %v", err)
	case <-ctx.Done():
		t.Fatal("worker did not claim the source before the deadline")
	}
	created, creation := readMockACPRequest(t, r.ws, "session/new")
	r.runs++
	native, err := json.Marshal(
		map[string]string{"sessionId": fmt.Sprintf("retry-native-%d", r.runs)},
	)
	require.NoError(t, err)
	r.sequence++
	writeMockACPResponse(t, r.ws, created.QueueID, r.sequence, creation.ID, native)
	envelope, prompt := readMockACPRequest(t, r.ws, "session/prompt")
	text := acpPromptText(prompt.Params)
	var input struct {
		Failure string `json:"previous_validation_failure"`
		Session string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(text[strings.LastIndex(text, "\n\n")+2:]), &input))
	assert.Equal(t, job.ValidationFeedback, input.Failure)
	assert.Equal(t, "source-session", input.Session)
	assert.Contains(t, text, "Use PostgreSQL.")
	sessionID := findStringFromRaw(prompt.Params, "sessionId")
	assert.Equal(t, secretary.ExecutionSessionID(job.OwnerUserID, job.RunID), sessionID)
	r.sequence++
	writeMockACPChunk(t, r.ws, envelope.QueueID, r.sequence, sessionID, answer)
	r.sequence++
	writeMockACPResponse(
		t,
		r.ws,
		envelope.QueueID,
		r.sequence,
		prompt.ID,
		json.RawMessage(`{"stopReason":"end_turn"}`),
	)
	select {
	case err = <-done:
		return job, sessionID, err
	case <-ctx.Done():
		t.Fatal("worker did not finish the completed ACP turn before the deadline")
		return job, sessionID, ctx.Err()
	}
}

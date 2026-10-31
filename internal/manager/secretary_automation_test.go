package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryObservedStore struct {
	domain.SecretaryStore
	published chan domain.SummaryVersion
}

func (s *secretaryObservedStore) PublishSecretaryJob(
	ctx context.Context,
	job domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	version, err := s.SecretaryStore.PublishSecretaryJob(ctx, job, request)
	if err == nil {
		s.published <- version
	}
	return version, err
}

func TestSecretaryAutomationGivenSelectedOwnedAgentThenMaintainsIncrementalFrozenSummaries(
	t *testing.T,
) {
	assertSecretaryAutomation(t, storage.NewMemoryStore(time.Now))
}

func assertSecretaryAutomation(t *testing.T, store Store) {
	t.Helper()
	srv := newServer(Config{LocalUserID: "local@example.test", AllowLocalUserHeader: true}, store)
	t.Cleanup(func() { require.NoError(t, srv.CloseTransportStore(context.Background())) })
	fixture := testNodeAgent(t, srv, "automatic-secretary@example.test")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	createConversationTestSession(t, srv, fixture, "source-session", "source-native")
	source := domain.Message{
		MessageID:   "secretary-source",
		AgentID:     fixture.agentID,
		SessionID:   "source-session",
		Role:        "user",
		MessageType: "user_message",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		SessionSeq:  1,
		TurnID:      "source-turn-1",
	}
	require.NoError(t, store.UpsertMessage(t.Context(), &source))
	for index, text := range []string{"data", "base"} {
		require.NoError(
			t,
			store.UpsertMessagePart(
				t.Context(),
				&domain.MessagePart{
					MessageID: source.MessageID,
					PartIndex: index,
					PartType:  domain.MessagePartText,
					Text:      text,
				},
			),
		)
	}
	jobs := store.(domain.SecretaryStore)
	observed := &secretaryObservedStore{
		SecretaryStore: jobs,
		published:      make(chan domain.SummaryVersion, 4),
	}
	worker := secretary.NewWorker(
		observed,
		srv.secretaryExecutor(),
		secretary.WorkerConfig{
			SettleDuration:      time.Nanosecond,
			PollInterval:        5 * time.Millisecond,
			RetryInterval:       5 * time.Millisecond,
			ExecutionTimeout:    10 * time.Second,
			ActiveCheckInterval: time.Millisecond,
			OnError: func(err error) {
				t.Logf("Secretary worker error: %v", err)
			},
		},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()), "default-off never schedules a model")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	mux.Handle("/", srv.routes())
	server := httptest.NewServer(mux)
	defer server.Close()
	assertProgress := func(completed, total int, state string) {
		t.Helper()
		request, requestErr := http.NewRequestWithContext(
			t.Context(), http.MethodGet,
			server.URL+"/api/v1/user/self/secretary/progress?limit=1", nil,
		)
		require.NoError(t, requestErr)
		request.Header.Set("X-User-Email", fixture.userEmail)
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		defer func() { _ = response.Body.Close() }()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var envelope struct {
			Data domain.SecretaryProgressPage `json:"data"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
		progress := envelope.Data
		assert.Equal(t, 1, progress.Counts.TotalSessions, "internal execution sessions never count")
		assert.Equal(t, total, progress.Counts.TotalMessages, "parts are not logical messages")
		assert.Equal(t, completed, progress.Counts.CompletedMessages)
		assert.Equal(t, total-completed, progress.Counts.PendingMessages)
		assert.Equal(t, total, progress.Counts.TotalTurns)
		assert.Equal(t, completed, progress.Counts.CompletedTurns)
		assert.Equal(t, total-completed, progress.Counts.PendingTurns)
		assert.Zero(t, progress.Counts.UngroupedMessages)
		require.Len(t, progress.Sessions, 1)
		assert.Equal(t, "source-session", progress.Sessions[0].SessionID)
		assert.Equal(t, state, progress.Sessions[0].State)
		assert.Equal(t, completed, progress.Sessions[0].CompletedMessages)
		assert.Equal(t, total-completed, progress.Sessions[0].PendingTurns)
		assert.Empty(t, progress.NextCursor)
	}
	assertProgress(0, 1, "unstarted")
	settings := func(enabled bool, revision int64) userapi.SecretaryResponse {
		t.Helper()
		raw, err := json.Marshal(
			domain.SecretarySettingsUpdate{
				AgentID:          fixture.agentID,
				Enabled:          enabled,
				ExpectedRevision: revision,
			},
		)
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodPatch,
			server.URL+"/api/v1/user/self/secretary",
			bytes.NewReader(raw),
		)
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-User-Email", fixture.userEmail)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var envelope struct {
			Data userapi.SecretaryResponse `json:"data"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
		return envelope.Data
	}
	cfg := settings(true, 0)
	require.True(t, cfg.Enabled)
	assert.Equal(t, "waiting_for_agent", cfg.State)
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
	assert.True(t, srv.secretaryAgentAvailable(t.Context(), principal.User.UserID, fixture.agentID))
	workerCtx, stopWorker := context.WithCancel(t.Context())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("automatic Secretary worker did not stop")
		}
	}()
	agentSequence := int64(0)
	run := func(expectedSource, summaryBody string, completed, total int) domain.SummaryVersion {
		t.Helper()
		created, createRequest := readMockACPRequest(t, ws, "session/new")
		agentSequence++
		native, marshalErr := json.Marshal(
			map[string]string{"sessionId": fmt.Sprintf("secretary-auto-native-%d", agentSequence)},
		)
		require.NoError(t, marshalErr)
		writeMockACPResponse(t, ws, created.QueueID, agentSequence, createRequest.ID, native)
		promptEnvelope, prompt := readMockACPRequest(t, ws, "session/prompt")
		assert.Contains(t, acpPromptText(prompt.Params), expectedSource)
		sessionID := findStringFromRaw(prompt.Params, "sessionId")
		assert.True(t, secretary.IsExecutionSession(sessionID))
		state := "unstarted"
		if completed > 0 {
			state = "partial"
		}
		assertProgress(completed, total, state)
		answer, marshalErr := json.Marshal(
			map[string]any{"title": "Automatic decision", "body": summaryBody},
		)
		require.NoError(t, marshalErr)
		agentSequence++
		writeMockACPChunk(t, ws, promptEnvelope.QueueID, agentSequence, sessionID, string(answer))
		agentSequence++
		writeMockACPResponse(
			t,
			ws,
			promptEnvelope.QueueID,
			agentSequence,
			prompt.ID,
			json.RawMessage(`{"stopReason":"end_turn"}`),
		)
		select {
		case version := <-observed.published:
			internal, readErr := store.GetSession(t.Context(), principal, sessionID)
			require.NoError(t, readErr)
			assert.Equal(t, domain.SecretaryInternalSource, internal.Source)
			sources := store.(domain.SearchSourceStore)
			_, readErr = sources.ReadSearchDocument(
				t.Context(),
				principal.User.UserID,
				"session",
				strconv.FormatInt(internal.ID, 10),
			)
			assert.ErrorIs(t, readErr, domain.ErrNotFound)
			messages, readErr := store.ListMessages(t.Context(), fixture.agentID, sessionID, 100)
			require.NoError(t, readErr)
			for _, message := range messages {
				_, readErr = sources.ReadSearchDocument(
					t.Context(),
					principal.User.UserID,
					"message",
					message.MessageID,
				)
				assert.ErrorIs(t, readErr, domain.ErrNotFound)
			}
			return version
		case <-time.After(15 * time.Second):
			t.Fatal("automatic worker did not persist a summary")
			return domain.SummaryVersion{}
		}
	}
	first := run("database", "Use the database.", 0, 1)
	require.Len(t, first.Coverage, 1)
	assert.Equal(t, source.MessageID, first.Coverage[0].MessageID)
	assert.Equal(t, int64(1), first.Revision)
	assertProgress(1, 1, "completed")
	status, err := jobs.GetSecretaryStatus(t.Context(), principal.User.UserID)
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
	require.NotNil(t, status.LastCompletedAt)
	select {
	case unexpected := <-observed.published:
		t.Fatalf("unchanged or internal source was summarized again: %s", unexpected.SummaryID)
	default:
	}
	require.NoError(
		t,
		store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: source.MessageID,
				PartIndex: 1,
				PartType:  domain.MessagePartText,
				Text:      "base v2",
			},
		),
	)
	assertProgress(0, 1, "unstarted")
	second := run("database v2", "Use database v2.", 0, 1)
	assert.Equal(t, first.SummaryID, second.SummaryID)
	assert.Equal(t, int64(2), second.Revision)
	assert.NotEqual(t, first.Coverage[0].ContentHash, second.Coverage[0].ContentHash)
	assertProgress(1, 1, "completed")
	summaries := store.(domain.SummaryStore)
	old, err := summaries.GetSummary(t.Context(), principal.User.UserID, first.SummaryID, 1)
	require.NoError(t, err)
	assert.Equal(t, "Use the database.", old.Body)
	frozen, err := summaries.ReadMessageVersion(t.Context(), principal.User.UserID, old.Coverage[0])
	require.NoError(t, err)
	assert.Equal(t, "database", frozen.Body)
	_, err = summaries.GetSummary(t.Context(), "different-owner", first.SummaryID, 0)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	added := domain.Message{
		MessageID:   "secretary-new-source",
		AgentID:     fixture.agentID,
		SessionID:   "source-session",
		Role:        "user",
		MessageType: "user_message",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionUserToAgent,
		SessionSeq:  2,
		TurnID:      "source-turn-2",
	}
	require.NoError(t, store.UpsertMessage(t.Context(), &added))
	require.NoError(
		t,
		store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: added.MessageID,
				PartIndex: 0,
				PartType:  domain.MessagePartText,
				Text:      "Add an index.",
			},
		),
	)
	assertProgress(1, 2, "partial")
	third := run("Add an index.", "Add the database index.", 1, 2)
	assert.NotEqual(t, first.SummaryID, third.SummaryID)
	require.Len(t, third.Coverage, 1)
	assert.Equal(t, added.MessageID, third.Coverage[0].MessageID)
	assertProgress(2, 2, "completed")
	settings(false, cfg.Revision)
	require.NoError(
		t,
		store.UpsertMessagePart(
			t.Context(),
			&domain.MessagePart{
				MessageID: added.MessageID,
				PartIndex: 0,
				PartType:  domain.MessagePartText,
				Text:      "Changed while disabled.",
			},
		),
	)
	require.NoError(
		t,
		worker.ProcessOnce(t.Context()),
		"disabled Secretary never starts another agent turn",
	)
	assertProgress(1, 2, "partial")
	select {
	case unexpected := <-observed.published:
		t.Fatalf("published while disabled: %s", unexpected.SummaryID)
	default:
	}
}

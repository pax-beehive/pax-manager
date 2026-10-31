package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type workspaceTestBackend struct {
	docs    []domain.SearchDocument
	ensured chan struct{}
}

func (b *workspaceTestBackend) EnsureIndex(context.Context) error {
	if b.ensured != nil {
		select {
		case b.ensured <- struct{}{}:
		default:
		}
	}
	return nil
}
func (b *workspaceTestBackend) Index(context.Context, domain.SearchDocument) error { return nil }

func (b *workspaceTestBackend) Search(
	context.Context,
	domain.SearchQuery,
) (domain.SearchPage, error) {
	return domain.SearchPage{Hits: b.docs}, nil
}

type workspaceSummaryFake struct{ sources domain.SearchSourceStore }

func (f workspaceSummaryFake) PublishSummary(
	_ context.Context,
	owner string,
	r domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	return domain.SummaryVersion{
		SummaryID:   r.SummaryID,
		OwnerUserID: owner,
		Revision:    1,
		Body:        r.Body,
	}, nil
}

func (f workspaceSummaryFake) GetSummary(
	context.Context,
	string,
	string,
	int64,
) (domain.SummaryVersion, error) {
	return domain.SummaryVersion{}, domain.ErrNotFound
}

func (f workspaceSummaryFake) ReadMessageVersion(
	ctx context.Context,
	owner string,
	r domain.MessageVersionRef,
) (domain.SearchDocument, error) {
	return f.sources.ReadSearchDocument(ctx, owner, "message", r.MessageID)
}

func TestWorkspaceHTTPGivenMultipartWhenSearchedAndReadThenReturnsStableOwnerScopedReference(
	t *testing.T,
) {
	srv, _ := testServer(t, "search@example.test")
	fixture := testNodeAgent(t, srv, "search@example.test")
	createConversationTestSession(t, srv, fixture, "search-session", "native-search-session")
	message := domain.Message{
		MessageID:   "search-message",
		AgentID:     fixture.agentID,
		SessionID:   "search-session",
		Role:        "assistant",
		Source:      domain.MessageSourceACPTunnel,
		Direction:   domain.MessageDirectionAgentToUser,
		MessageType: "agent_message_chunk",
	}
	require.NoError(t, srv.store.UpsertMessage(t.Context(), &message))
	for index, text := range []string{"\u6570\u636e", "\u5e93"} {
		require.NoError(
			t,
			srv.store.UpsertMessagePart(
				t.Context(),
				&domain.MessagePart{
					MessageID: message.MessageID,
					PartIndex: index,
					PartType:  domain.MessagePartText,
					Text:      text,
				},
			),
		)
	}
	source := srv.store.(canonicalSessionStore).Store.(domain.SearchSourceStore)
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(
		srv.auth,
		source,
		workspaceSummaryFake{sources: source},
		&workspaceTestBackend{
			docs: []domain.SearchDocument{{Kind: "message", SourceID: message.MessageID}},
		},
		[]byte(strings.Repeat("k", 32)),
	)
	handler := srv.routes()
	post := func(path, email string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data)))
		request.Header.Set("X-User-Email", email)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	response := post(
		"/api/v1/user/self/search",
		fixture.userEmail,
		map[string]any{"query": "\u6570\u636e"},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page := decodeData[userapi.WorkspaceSearchPage](t, response.Body.Bytes())
	require.Len(t, page.Hits, 1)
	assert.Equal(t, "\u6570\u636e\u5e93", page.Hits[0].Snippet)
	response = post(
		"/api/v1/user/self/search/read",
		fixture.userEmail,
		userapi.WorkspaceReadRequest{Ref: page.Hits[0].Ref, View: "full"},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	read := decodeData[userapi.WorkspaceMessageRead](t, response.Body.Bytes())
	assert.Equal(t, "\u6570\u636e\u5e93", read.Text)
	assert.NotEmpty(t, read.ReadReceipt)
	response = post(
		"/api/v1/user/self/search/summaries",
		fixture.userEmail,
		domain.PublishSummaryRequest{
			SummaryID:   "s1",
			SubjectType: "topic",
			SubjectID:   "topic",
			RequestID:   "request",
			Body:        "summary",
			Coverage: []domain.MessageVersionRef{
				{
					MessageID:               read.MessageID,
					ContentHash:             read.Ref.ContentHash,
					CanonicalizationVersion: read.Ref.CanonicalizationVersion,
					ReadReceipt:             read.ReadReceipt,
				},
			},
		},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, int64(1), decodeData[domain.SummaryVersion](t, response.Body.Bytes()).Revision)
	response = post(
		"/api/v1/user/self/search",
		"stranger@example.test",
		map[string]any{"query": "\u6570\u636e"},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Empty(t, decodeData[userapi.WorkspaceSearchPage](t, response.Body.Bytes()).Hits)
	for _, path := range []string{"/api/v1/user/self/search", "/api/v1/user/self/search/read", "/api/v1/user/self/search/summaries"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{"))
		request.Header.Set("X-User-Email", fixture.userEmail)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

type workspaceQueueFake struct {
	Store
	backfills atomic.Int32
}

func (q *workspaceQueueFake) BackfillSearch(
	context.Context,
) error {
	q.backfills.Add(1)
	return nil
}

func (q *workspaceQueueFake) ClaimSearchBatch(
	context.Context,
	int,
	time.Duration,
) ([]domain.SearchWork, error) {
	return nil, nil
}
func (q *workspaceQueueFake) CompleteSearchWork(context.Context, domain.SearchWork, error) error {
	return nil
}

func TestWorkspaceSearchWorkerGivenConfiguredThenRunsWithinApplicationLifetime(t *testing.T) {
	s := &Service{}
	s.startWorkspaceSearchWorker(t.Context(), nil)()
	s.cfg.SearchURL = "http://localhost:9200"
	s.configureWorkspaceSearch(nil)
	assert.NotNil(t, s.searchBackend)
	assert.NotNil(t, s.workspaceSearch)
	s.cfg.SearchURL = ":invalid"
	s.configureWorkspaceSearch(nil)
	backend := &workspaceTestBackend{ensured: make(chan struct{}, 1)}
	s.searchBackend = backend
	queue := &workspaceQueueFake{}
	stop := s.startWorkspaceSearchWorker(t.Context(), queue)
	select {
	case <-backend.ensured:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	stop()
	assert.Equal(t, int32(1), queue.backfills.Load())
}

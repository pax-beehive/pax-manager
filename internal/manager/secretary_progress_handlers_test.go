package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryProgressHTTPStore struct {
	domain.SecretaryStore
	page  domain.SecretaryProgressPage
	err   error
	owner string
	query domain.SecretaryProgressQuery
	calls int
}

func (s *secretaryProgressHTTPStore) ReadSecretaryProgress(
	_ context.Context,
	owner string,
	query domain.SecretaryProgressQuery,
) (domain.SecretaryProgressPage, error) {
	s.owner, s.query = owner, query
	s.calls++
	return s.page, s.err
}

func TestSecretaryProgressHTTPGivenOwnerThenReturnsGlobalCountsAndBoundedPage(t *testing.T) {
	srv, _ := testServer(t, "progress-owner@example.test")
	principal := testUserPrincipal(t, srv, "progress-owner@example.test")
	store := &secretaryProgressHTTPStore{page: domain.SecretaryProgressPage{
		AsOf: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		Counts: domain.SecretaryProgressCounts{
			SecretaryCoverageCounts: domain.SecretaryCoverageCounts{
				TotalMessages:     500,
				CompletedMessages: 300,
				PendingMessages:   200,
			},
			TotalSessions: 80,
		},
		Sessions: []domain.SecretarySessionProgress{
			{
				SourceID:  "12",
				AgentID:   "agent",
				SessionID: "session",
				Title:     "Source",
				State:     "partial",
			},
		}, NextCursor: "next-page",
	}}
	srv.secretary = userapi.NewSecretaryService(srv.auth, store, nil, nil)
	handler := srv.routes()
	for _, test := range []struct {
		query  string
		limit  int
		cursor string
	}{
		{"", 20, ""}, {"?limit=1&cursor=opaque", 1, "opaque"}, {"?limit=100", 100, ""}, {"?cursor=", 20, ""},
	} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/secretary/progress"+test.query,
			nil,
		)
		request.Header.Set("X-User-Email", "progress-owner@example.test")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		page := decodeData[domain.SecretaryProgressPage](t, recorder.Body.Bytes())
		assert.Equal(t, 500, page.Counts.TotalMessages)
		assert.Len(t, page.Sessions, 1)
		assert.Equal(t, "next-page", page.NextCursor)
		assert.Equal(t, principal.User.UserID, store.owner)
		assert.Equal(t, test.limit, store.query.Limit)
		assert.Equal(t, test.cursor, store.query.Cursor)
	}
}

func TestSecretaryProgressHTTPGivenMalformedQueryThenRejectsWithoutStorageRead(t *testing.T) {
	srv, _ := testServer(t, "progress-invalid@example.test")
	store := &secretaryProgressHTTPStore{}
	srv.secretary = userapi.NewSecretaryService(srv.auth, store, nil, nil)
	handler := srv.routes()
	for _, query := range []string{
		"?limit", "?limit=", "?limit=0", "?limit=-1", "?limit=101", "?limit=1.5", "?limit=no", "?limit=999999999999999999999999999999",
		"?limit=1&limit=1", "?cursor=a&cursor=b", "?owner_user_id=victim", "?cursor=%0A", "?cursor=%E6%95%B0",
	} {
		t.Run(query, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/user/self/secretary/progress"+query,
				nil,
			)
			request.Header.Set("X-User-Email", "progress-invalid@example.test")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
	assert.Zero(t, store.calls)
}

func TestSecretaryProgressHTTPGivenCursorOrIdentityFailureThenFailsClosed(t *testing.T) {
	srv, _ := testServer(t, "progress-failure@example.test")
	store := &secretaryProgressHTTPStore{err: domain.ErrInvalidCursor}
	srv.secretary = userapi.NewSecretaryService(srv.auth, store, nil, nil)
	handler := srv.routes()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/secretary/progress?cursor=invalid",
		nil,
	)
	request.Header.Set("X-User-Email", "progress-failure@example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	srv.secretary = userapi.NewSecretaryService(nil, store, nil, nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
	srv.secretary = userapi.NewSecretaryService(srv.auth, nil, nil, nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
}

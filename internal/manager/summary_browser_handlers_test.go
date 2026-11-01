package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type developerSummaryHTTPStore struct {
	domain.SummaryStore
	page     domain.SummaryListPage
	summary  domain.SummaryVersion
	err      error
	owner    string
	query    domain.SummaryListQuery
	id       string
	revision int64
	calls    int
}

func (s *developerSummaryHTTPStore) ListCurrentSummaries(
	_ context.Context,
	owner string,
	query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	s.owner, s.query = owner, query
	s.calls++
	return s.page, s.err
}

func (s *developerSummaryHTTPStore) GetSummary(
	_ context.Context,
	owner, id string,
	revision int64,
) (domain.SummaryVersion, error) {
	s.owner, s.id, s.revision = owner, id, revision
	s.calls++
	return s.summary, s.err
}

func TestDeveloperSummaryHTTPGivenAdminThenBrowsesAndReadsOwnRevisions(t *testing.T) {
	srv, _ := testServer(t, "admin@example.com")
	principal := testUserPrincipal(t, srv, "admin@example.com")
	store := &developerSummaryHTTPStore{
		page: domain.SummaryListPage{
			Items:      []domain.SummaryListItem{{SummaryID: "s", Revision: 2}},
			NextCursor: "next",
		},
		summary: domain.SummaryVersion{
			SummaryID:   "s",
			OwnerUserID: principal.User.UserID,
			Revision:    2,
			Body:        "Full structured body",
		},
	}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	for _, query := range []string{"", "?limit=1&cursor=opaque", "?limit=100", "?cursor="} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/developer/summaries"+query,
			nil,
		)
		request.Header.Set("X-User-Email", "admin@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		page := decodeData[domain.SummaryListPage](t, recorder.Body.Bytes())
		assert.Equal(t, store.page, page)
		assert.Equal(t, principal.User.UserID, store.owner)
	}
	for _, test := range []struct {
		query    string
		revision int64
	}{{"", 0}, {"?revision=0", 0}, {"?revision=2", 2}, {"?revision=9223372036854775807", 9223372036854775807}} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/developer/summaries/s"+test.query,
			nil,
		)
		request.Header.Set("X-User-Email", "admin@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		read := decodeData[userapi.DeveloperSummaryRead](t, recorder.Body.Bytes())
		assert.Equal(t, "Full structured body", read.Summary.Body)
		assert.Equal(t, principal.User.UserID, store.owner)
		assert.Equal(t, "s", store.id)
		assert.Equal(t, test.revision, store.revision)
	}
}

func TestDeveloperSummaryHTTPGivenOrdinaryUserThenDirectRoutesReturnForbidden(t *testing.T) {
	srv, _ := testServer(t, "viewer@example.com")
	store := &developerSummaryHTTPStore{}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	for _, path := range []string{"/api/v1/user/self/developer/summaries", "/api/v1/user/self/developer/summaries/s?revision=1"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-User-Email", "viewer@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	}
	assert.Zero(t, store.calls)
}

func TestDeveloperSummaryHTTPGivenLongIdentityAndCursorThenRetainsPersistedContract(t *testing.T) {
	srv, _ := testServer(t, "admin@example.com")
	id := strings.Repeat("a", 5000)
	store := &developerSummaryHTTPStore{summary: domain.SummaryVersion{SummaryID: id, Revision: 1}}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/developer/summaries/"+id+"?revision=1",
		nil,
	)
	request.Header.Set("X-User-Email", "admin@example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, id, store.id)
	cursor := strings.Repeat("a", 7000)
	request = httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/developer/summaries?cursor="+cursor,
		nil,
	)
	request.Header.Set("X-User-Email", "admin@example.com")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, cursor, store.query.Cursor)
}

func TestDeveloperSummaryHTTPGivenMalformedQueryThenRejectsWithoutReading(t *testing.T) {
	srv, _ := testServer(t, "admin@example.com")
	store := &developerSummaryHTTPStore{}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	for _, query := range []string{"?limit", "?limit=", "?limit=0", "?limit=-1", "?limit=101", "?limit=1.5", "?limit=no", "?limit=999999999999999999999999999", "?limit=1&limit=2", "?cursor=a&cursor=b", "?owner_user_id=other"} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/developer/summaries"+query,
			nil,
		)
		request.Header.Set("X-User-Email", "admin@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	for _, query := range []string{"?revision", "?revision=", "?revision=-1", "?revision=1.0", "?revision=x", "?revision=9223372036854775808", "?revision=1&revision=1", "?owner_user_id=other", "?revision=1&other=x"} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/developer/summaries/s"+query,
			nil,
		)
		request.Header.Set("X-User-Email", "admin@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	assert.Zero(t, store.calls)
}

func TestDeveloperSummaryHTTPGivenInvalidCursorOrHiddenRevisionThenReturnsSafeStatus(t *testing.T) {
	srv, _ := testServer(t, "admin@example.com")
	store := &developerSummaryHTTPStore{err: domain.ErrInvalidCursor}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/developer/summaries?cursor=bad",
		nil,
	)
	request.Header.Set("X-User-Email", "admin@example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	store.err = domain.ErrNotFound
	request = httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/developer/summaries/foreign?revision=1",
		nil,
	)
	request.Header.Set("X-User-Email", "admin@example.com")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
}

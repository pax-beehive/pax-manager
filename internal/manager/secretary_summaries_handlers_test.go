package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretarySummaryHTTPStore struct{ developerSummaryHTTPStore }

func (s *secretarySummaryHTTPStore) ListRecentSummaries(
	ctx context.Context, owner string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	return s.ListCurrentSummaries(ctx, owner, query)
}

func TestSecretarySummariesHTTPGivenOrdinaryOwnerThenBrowsesOwnResults(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	principal := testUserPrincipal(t, srv, "owner@example.com")
	store := &secretarySummaryHTTPStore{developerSummaryHTTPStore{page: domain.SummaryListPage{
		Items: []domain.SummaryListItem{
			{SummaryID: "s", Revision: 3, Title: "Search decisions"},
		}, NextCursor: "next",
	}}}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	for _, query := range []string{"", "?limit=1&cursor=opaque", "?limit=100"} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/secretary/summaries"+query,
			nil,
		)
		request.Header.Set("X-User-Email", "owner@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, store.page, decodeData[domain.SummaryListPage](t, recorder.Body.Bytes()))
		assert.Equal(t, principal.User.UserID, store.owner)
	}
}

func TestSecretarySummariesHTTPGivenMalformedQueryThenRejectsBeforeStorage(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	store := &secretarySummaryHTTPStore{}
	srv.workspaceSearch = userapi.NewWorkspaceSearchService(srv.auth, nil, store, nil, nil)
	handler := srv.routes()
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=", "?limit=1&limit=2", "?revision=1", "?cursor=a&cursor=b"} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/secretary/summaries"+query,
			nil,
		)
		request.Header.Set("X-User-Email", "owner@example.com")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	assert.Zero(t, store.calls)
}

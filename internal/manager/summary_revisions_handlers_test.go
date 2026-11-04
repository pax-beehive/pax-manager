package manager

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func summaryRevisionHTTPFixture(t *testing.T) (*Service, string, string) {
	t.Helper()
	const email = "summary-owner@example.test"
	srv, _ := testServer(t, email)
	fixture := testNodeAgent(t, srv, email)
	owner := testUserPrincipal(t, srv, email).User.UserID
	createConversationTestSession(t, srv, fixture, "source-session", "source-native")
	putSecretaryToolMessage(t, srv, fixture, "summary-source", "user", "Original decision.")
	store := srv.secretaryStore.(domain.SummaryStore)
	for revision := int64(1); revision <= 3; revision++ {
		_, err := store.PublishSummary(t.Context(), owner, domain.PublishSummaryRequest{
			SummaryID: "summary-history", SubjectType: "fragment", SubjectID: "source-session",
			ExpectedRevision: revision - 1, RequestID: "publish-" + strconv.FormatInt(revision, 10),
			Title: "Revision " + strconv.FormatInt(
				revision,
				10,
			), Body: "Decision revision " + strconv.FormatInt(revision, 10),
			Coverage: []domain.MessageVersionRef{
				{
					MessageID:               "summary-source",
					ContentHash:             domain.SearchContentHash("Original decision."),
					CanonicalizationVersion: domain.SearchCanonicalizationVersion,
				},
			},
		})
		require.NoError(t, err)
	}
	return srv, email, owner
}

func TestSummaryRevisionHTTPGivenOwnerThenPaginatesExactVersionsWithoutAdminRole(t *testing.T) {
	srv, email, _ := summaryRevisionHTTPFixture(t)
	handler := srv.routes()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/secretary/summaries/summary-history/revisions?limit=2",
		nil,
	)
	request.Header.Set("X-User-Email", email)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page := decodeData[domain.SummaryListPage](t, response.Body.Bytes())
	require.Len(t, page.Items, 2)
	assert.Equal(t, int64(3), page.Items[0].Revision)
	assert.Equal(t, "Revision 3", page.Items[0].Title)
	assert.Equal(t, int64(2), page.Items[1].Revision)
	require.NotEmpty(t, page.NextCursor)
	request = httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/secretary/summaries/summary-history/revisions?limit=2&cursor="+url.QueryEscape(
			page.NextCursor,
		),
		nil,
	)
	request.Header.Set("X-User-Email", email)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page = decodeData[domain.SummaryListPage](t, response.Body.Bytes())
	require.Len(t, page.Items, 1)
	assert.Equal(t, int64(1), page.Items[0].Revision)
	assert.Equal(t, "Decision revision 1", page.Items[0].BodyPreview)
	assert.Empty(t, page.NextCursor)
}

func TestSummaryRevisionHTTPGivenForeignOwnerThenHidesSummaryAndRejectsIdentityOverride(
	t *testing.T,
) {
	srv, email, owner := summaryRevisionHTTPFixture(t)
	handler := srv.routes()
	for _, path := range []string{
		"/api/v1/user/self/secretary/summaries/summary-history/revisions",
		"/api/v1/user/" + owner + "/secretary/summaries/summary-history/revisions",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-User-Email", "other@example.test")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "Decision revision")
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/secretary/summaries/missing/revisions",
		nil,
	)
	request.Header.Set("X-User-Email", email)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
}

func TestSummaryRevisionHTTPGivenMalformedOrDuplicateParametersThenRejects(t *testing.T) {
	srv, email, _ := summaryRevisionHTTPFixture(t)
	handler := srv.routes()
	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=101", "?limit=1.5", "?limit=1&limit=2", "?cursor=a&cursor=b", "?owner_user_id=other", "?cursor=invalid"} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/secretary/summaries/summary-history/revisions"+query,
			nil,
		)
		request.Header.Set("X-User-Email", email)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), "Decision revision")
	}
}

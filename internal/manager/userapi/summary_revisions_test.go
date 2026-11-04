package userapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type summaryRevisionsFixture struct {
	summaryBrowserFixture
	id string
}

func TestSummaryRevisionsGivenEmptyIdentityOrDeniedPrincipalThenDoesNotReadStorage(t *testing.T) {
	for _, id := range []string{"", " \t\n"} {
		store := &summaryRevisionsFixture{}
		service := NewWorkspaceSearchService(
			workspacePrincipal{owner: "owner"},
			nil,
			store,
			nil,
			nil,
		)
		_, err := service.ListSummaryRevisions(
			t.Context(),
			auth.RequestMetadata{},
			id,
			domain.SummaryListQuery{},
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadRequest, httpErr.Status)
		assert.Zero(t, store.calls)
	}
	store := &summaryRevisionsFixture{}
	service := NewWorkspaceSearchService(
		summaryAdminPrincipal{owner: "owner", err: domain.ErrUnauthorized},
		nil,
		store,
		nil,
		nil,
	)
	_, err := service.ListSummaryRevisions(
		t.Context(),
		auth.RequestMetadata{},
		"summary",
		domain.SummaryListQuery{},
	)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Zero(t, store.calls)
}

func TestSummaryRevisionsGivenInvalidCursorOrUnavailableSourceThenReturnsSafeError(t *testing.T) {
	backendErr := errors.New("summary backend unavailable")
	for name, failure := range map[string]error{
		"cursor":         domain.ErrInvalidCursor,
		"hidden summary": domain.ErrNotFound,
		"denied source":  domain.ErrUnauthorized,
		"backend":        backendErr,
	} {
		t.Run(name, func(t *testing.T) {
			store := &summaryRevisionsFixture{}
			store.err = failure
			service := NewWorkspaceSearchService(
				workspacePrincipal{owner: "owner"},
				nil,
				store,
				nil,
				nil,
			)
			_, err := service.ListSummaryRevisions(
				t.Context(),
				auth.RequestMetadata{},
				"summary",
				domain.SummaryListQuery{Cursor: "opaque", Limit: 3},
			)
			if errors.Is(failure, domain.ErrInvalidCursor) {
				var httpErr apperr.Error
				require.ErrorAs(t, err, &httpErr)
				assert.Equal(t, http.StatusBadRequest, httpErr.Status)
			} else {
				assert.ErrorIs(t, err, failure)
			}
			assert.Equal(t, domain.SummaryListQuery{Cursor: "opaque", Limit: 3}, store.query)
			assert.Equal(t, 1, store.calls)
		})
	}
}

func (f *summaryRevisionsFixture) ListSummaryRevisions(
	ctx context.Context,
	owner, id string,
	query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	f.id = id
	return f.ListCurrentSummaries(ctx, owner, query)
}

func TestSummaryRevisionsGivenOwnerThenReturnsPagedExactVersions(t *testing.T) {
	store := &summaryRevisionsFixture{
		summaryBrowserFixture: summaryBrowserFixture{
			page: domain.SummaryListPage{
				Items: []domain.SummaryListItem{
					{SummaryID: "summary", Revision: 4},
					{SummaryID: "summary", Revision: 2},
				},
				NextCursor: "next",
			},
		},
	}
	service := NewWorkspaceSearchService(workspacePrincipal{owner: "owner"}, nil, store, nil, nil)
	page, err := service.ListSummaryRevisions(
		t.Context(),
		auth.RequestMetadata{},
		"summary",
		domain.SummaryListQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, store.page, page)
	assert.Equal(t, "summary", store.id)
	assert.Equal(t, "owner", store.owner)
	assert.Equal(t, 20, store.query.Limit)
}

func TestSummaryRevisionsGivenInvalidRequestThenFailsBeforeStorage(t *testing.T) {
	for _, query := range []domain.SummaryListQuery{{Limit: -1}, {Limit: 101}} {
		store := &summaryRevisionsFixture{}
		service := NewWorkspaceSearchService(
			workspacePrincipal{owner: "owner"},
			nil,
			store,
			nil,
			nil,
		)
		_, err := service.ListSummaryRevisions(
			t.Context(),
			auth.RequestMetadata{},
			"summary",
			query,
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadRequest, httpErr.Status)
		assert.Zero(t, store.calls)
	}
	service := NewWorkspaceSearchService(nil, nil, &summaryRevisionsFixture{}, nil, nil)
	_, err := service.ListSummaryRevisions(
		t.Context(),
		auth.RequestMetadata{},
		"summary",
		domain.SummaryListQuery{},
	)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	service = NewWorkspaceSearchService(workspacePrincipal{owner: "owner"}, nil, nil, nil, nil)
	_, err = service.ListSummaryRevisions(
		t.Context(),
		auth.RequestMetadata{},
		"summary",
		domain.SummaryListQuery{},
	)
	var httpErr apperr.Error
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.Status)
}

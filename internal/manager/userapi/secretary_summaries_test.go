package userapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretarySummaryBrowserFixture struct{ summaryBrowserFixture }

func (f *secretarySummaryBrowserFixture) ListRecentSummaries(
	ctx context.Context, owner string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	return f.ListCurrentSummaries(ctx, owner, query)
}

func TestSecretarySummariesGivenOrdinaryOwnerThenListsWithoutAdminOrSearch(t *testing.T) {
	store := &secretarySummaryBrowserFixture{summaryBrowserFixture{page: domain.SummaryListPage{
		Items: []domain.SummaryListItem{{SummaryID: "summary", Revision: 3}}, NextCursor: "next",
	}}}
	service := NewWorkspaceSearchService(workspacePrincipal{owner: "owner"}, nil, store, nil, nil)
	page, err := service.ListSecretarySummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, store.page, page)
	assert.Equal(t, "owner", store.owner)
	assert.Equal(t, 20, store.query.Limit)
	_, err = service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	var httpErr apperr.Error
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusForbidden, httpErr.Status)
}

func TestSecretarySummariesGivenInvalidIdentityQueryOrStorageThenFailsClosed(t *testing.T) {
	for _, principal := range []PrincipalResolver{nil, workspacePrincipal{}, workspacePrincipal{err: domain.ErrUnauthorized}} {
		store := &secretarySummaryBrowserFixture{}
		service := NewWorkspaceSearchService(principal, nil, store, nil, nil)
		_, err := service.ListSecretarySummaries(
			t.Context(),
			auth.RequestMetadata{},
			domain.SummaryListQuery{},
		)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		assert.Zero(t, store.calls)
	}
	for _, limit := range []int{-1, 101} {
		service := NewWorkspaceSearchService(
			workspacePrincipal{owner: "owner"},
			nil,
			&secretarySummaryBrowserFixture{},
			nil,
			nil,
		)
		_, err := service.ListSecretarySummaries(
			t.Context(),
			auth.RequestMetadata{},
			domain.SummaryListQuery{Limit: limit},
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	}
	for _, store := range []domain.SummaryStore{nil, &workspaceSources{}} {
		service := NewWorkspaceSearchService(
			workspacePrincipal{owner: "owner"},
			nil,
			store,
			nil,
			nil,
		)
		_, err := service.ListSecretarySummaries(
			t.Context(),
			auth.RequestMetadata{},
			domain.SummaryListQuery{},
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusServiceUnavailable, httpErr.Status)
	}
	store := &secretarySummaryBrowserFixture{
		summaryBrowserFixture{workspaceSources: workspaceSources{err: domain.ErrInvalidCursor}},
	}
	service := NewWorkspaceSearchService(workspacePrincipal{owner: "owner"}, nil, store, nil, nil)
	_, err := service.ListSecretarySummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{Cursor: "bad", Limit: 1},
	)
	var httpErr apperr.Error
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	assert.Equal(t, "bad", store.query.Cursor)
	assert.Equal(t, 1, store.query.Limit)
}

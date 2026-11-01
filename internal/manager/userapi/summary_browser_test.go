package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type summaryAdminPrincipal struct {
	admin bool
	owner string
	err   error
}

func (p summaryAdminPrincipal) Principal(
	context.Context,
	auth.RequestMetadata,
) (domain.UserPrincipal, error) {
	return domain.UserPrincipal{User: domain.User{UserID: p.owner}, IsAdmin: p.admin}, p.err
}

type summaryBrowserFixture struct {
	workspaceSources
	owner string
	query domain.SummaryListQuery
	calls int
	page  domain.SummaryListPage
}

func (f *summaryBrowserFixture) ListCurrentSummaries(
	_ context.Context,
	owner string,
	query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	f.owner, f.query = owner, query
	f.calls++
	return f.page, f.err
}

func TestSummaryBrowserGivenAdminThenListsOwnCurrentSummariesWithoutSearchBackend(t *testing.T) {
	store := &summaryBrowserFixture{
		page: domain.SummaryListPage{
			Items:      []domain.SummaryListItem{{SummaryID: "s", Revision: 2}},
			NextCursor: "next",
		},
	}
	service := NewWorkspaceSearchService(
		summaryAdminPrincipal{admin: true, owner: "admin"},
		nil,
		store,
		nil,
		nil,
	)
	page, err := service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, store.page, page)
	assert.Equal(t, "admin", store.owner)
	assert.Equal(t, 20, store.query.Limit)
}

func TestSummaryBrowserGivenNonAdminOrNoIdentityThenRejectsBeforeStorage(t *testing.T) {
	for _, principal := range []summaryAdminPrincipal{{owner: "user"}, {admin: true}, {owner: "admin", admin: true, err: domain.ErrUnauthorized}} {
		store := &summaryBrowserFixture{}
		service := NewWorkspaceSearchService(principal, nil, store, nil, nil)
		_, err := service.ListDeveloperSummaries(
			t.Context(),
			auth.RequestMetadata{},
			domain.SummaryListQuery{},
		)
		require.Error(t, err)
		_, readErr := service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, "s", 0)
		require.Error(t, readErr)
		assert.Zero(t, store.calls)
		if principal.owner == "user" {
			var httpErr apperr.Error
			require.ErrorAs(t, err, &httpErr)
			assert.Equal(t, http.StatusForbidden, httpErr.Status)
		}
	}
}

func TestSummaryBrowserGivenExactRevisionThenReadsThroughExistingOwnerScopedStore(t *testing.T) {
	store := &summaryBrowserFixture{
		workspaceSources: workspaceSources{
			summary: domain.SummaryVersion{
				SummaryID:   "s",
				OwnerUserID: "admin",
				Revision:    2,
				Body:        "exact body",
			},
		},
	}
	service := NewWorkspaceSearchService(
		summaryAdminPrincipal{owner: "admin", admin: true},
		nil,
		store,
		nil,
		nil,
	)
	for _, revision := range []int64{0, 2} {
		read, err := service.ReadDeveloperSummary(
			t.Context(),
			auth.RequestMetadata{},
			"s",
			revision,
		)
		require.NoError(t, err)
		assert.Equal(t, "exact body", read.Summary.Body)
	}
	_, err := service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, "s", 1)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	store.summary.OwnerUserID = "other"
	_, err = service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, "s", 2)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSummaryBrowserGivenLongPersistedIdentityThenReadsAndPassesCursorToStorage(t *testing.T) {
	id := strings.Repeat("a", 5000)
	store := &summaryBrowserFixture{
		workspaceSources: workspaceSources{summary: domain.SummaryVersion{
			SummaryID: id, OwnerUserID: "admin", Revision: 1, Body: "long identity summary",
		}},
	}
	service := NewWorkspaceSearchService(
		summaryAdminPrincipal{owner: "admin", admin: true},
		nil,
		store,
		nil,
		nil,
	)
	read, err := service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, id, 1)
	require.NoError(t, err)
	assert.Equal(t, id, read.Summary.SummaryID)
	cursor := strings.Repeat("a", 7000)
	_, err = service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{Cursor: cursor},
	)
	require.NoError(t, err)
	assert.Equal(t, cursor, store.query.Cursor)
}

func TestSummaryBrowserGivenInvalidQueryOrUnavailableStorageThenReturnsUsefulError(t *testing.T) {
	principal := summaryAdminPrincipal{owner: "admin", admin: true}
	service := NewWorkspaceSearchService(principal, nil, nil, nil, nil)
	_, err := service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	var httpErr apperr.Error
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.Status)
	_, err = service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, "s", 0)
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.Status)
	service = NewWorkspaceSearchService(principal, nil, &workspaceSources{}, nil, nil)
	_, err = service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.Status)
	service = NewWorkspaceSearchService(principal, nil, &summaryBrowserFixture{}, nil, nil)
	for _, query := range []domain.SummaryListQuery{{Limit: -1}, {Limit: 101}} {
		_, err = service.ListDeveloperSummaries(t.Context(), auth.RequestMetadata{}, query)
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	}
	for _, id := range []string{"", "  "} {
		_, err = service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, id, 0)
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	}
	_, err = service.ReadDeveloperSummary(t.Context(), auth.RequestMetadata{}, "s", -1)
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	service = NewWorkspaceSearchService(nil, nil, &summaryBrowserFixture{}, nil, nil)
	_, err = service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	store := &summaryBrowserFixture{
		workspaceSources: workspaceSources{err: domain.ErrInvalidCursor},
	}
	service = NewWorkspaceSearchService(principal, nil, store, nil, nil)
	_, err = service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{Cursor: "bad"},
	)
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.Status)
	store.err = errors.New("database unavailable")
	_, err = service.ListDeveloperSummaries(
		t.Context(),
		auth.RequestMetadata{},
		domain.SummaryListQuery{},
	)
	assert.ErrorContains(t, err, "database unavailable")
}

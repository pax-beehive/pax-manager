package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryProgressFixture struct {
	domain.SecretaryStore
	page  domain.SecretaryProgressPage
	err   error
	owner string
	query domain.SecretaryProgressQuery
	ctx   context.Context
	calls int
}

func (f *secretaryProgressFixture) ReadSecretaryProgress(
	ctx context.Context,
	owner string,
	query domain.SecretaryProgressQuery,
) (domain.SecretaryProgressPage, error) {
	f.ctx, f.owner, f.query = ctx, owner, query
	f.calls++
	return f.page, f.err
}

func TestSecretaryProgressGivenAuthenticatedOwnerThenReturnsGlobalCountsAndPagedSources(
	t *testing.T,
) {
	store := &secretaryProgressFixture{page: domain.SecretaryProgressPage{
		AsOf: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		Counts: domain.SecretaryProgressCounts{
			SecretaryCoverageCounts: domain.SecretaryCoverageCounts{
				TotalMessages:     500,
				CompletedMessages: 300,
				PendingMessages:   200,
			},
			TotalSessions:     80,
			CompletedSessions: 20,
			PartialSessions:   30,
			UnstartedSessions: 30,
		},
		Sessions: []domain.SecretarySessionProgress{
			{
				SourceID:  "12",
				AgentID:   "source-agent",
				SessionID: "session",
				Title:     "Progress",
				State:     "partial",
				SecretaryCoverageCounts: domain.SecretaryCoverageCounts{
					TotalMessages:     10,
					CompletedMessages: 3,
					PendingMessages:   7,
				},
			},
		}, NextCursor: "next",
	}}
	service := NewSecretaryService(
		workspacePrincipal{owner: "authenticated-owner"},
		store,
		nil,
		nil,
	)
	query := domain.SecretaryProgressQuery{Cursor: "opaque", Limit: 2}
	result, err := service.Progress(t.Context(), auth.RequestMetadata{}, query)
	require.NoError(t, err)
	assert.Equal(t, "authenticated-owner", store.owner)
	assert.Equal(t, query, store.query)
	assert.Equal(t, store.page, result)
	assert.Equal(
		t,
		500,
		result.Counts.TotalMessages,
		"global totals must not be inferred from the current page",
	)
	assert.Same(t, t.Context(), store.ctx)
}

func TestSecretaryProgressGivenDefaultsThenUsesBoundedPageAndStableEmptyArray(t *testing.T) {
	store := &secretaryProgressFixture{}
	service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
	page, err := service.Progress(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretaryProgressQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, 20, store.query.Limit)
	assert.NotNil(t, page.Sessions)
	assert.Empty(t, page.Sessions)
}

func TestSecretaryProgressGivenInvalidQueryThenRejectsBeforeStorageRead(t *testing.T) {
	for _, query := range []domain.SecretaryProgressQuery{
		{Limit: -1}, {Limit: 101}, {Cursor: strings.Repeat("x", 2049)}, {Cursor: " leading"}, {Cursor: "trailing "}, {Cursor: "line\nfeed"}, {Cursor: "\x00"}, {Cursor: "\u6570"},
	} {
		store := &secretaryProgressFixture{}
		service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
		_, err := service.Progress(t.Context(), auth.RequestMetadata{}, query)
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusBadRequest, apiErr.Status)
		assert.Zero(t, store.calls)
	}
}

func TestSecretaryProgressGivenCursorOrStorageFailureThenPreservesErrorSemantics(t *testing.T) {
	store := &secretaryProgressFixture{err: domain.ErrInvalidCursor}
	service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
	_, err := service.Progress(
		t.Context(),
		auth.RequestMetadata{},
		domain.SecretaryProgressQuery{Cursor: "invalid"},
	)
	var apiErr apperr.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Status)
	store.err = errors.New("database unavailable")
	_, err = service.Progress(t.Context(), auth.RequestMetadata{}, domain.SecretaryProgressQuery{})
	assert.ErrorIs(t, err, store.err)
}

func TestSecretaryProgressGivenMissingIdentityOrCapabilityThenFailsClosed(t *testing.T) {
	for _, principal := range []PrincipalResolver{nil, workspacePrincipal{}, workspacePrincipal{err: domain.ErrUnauthorized}} {
		service := NewSecretaryService(principal, &secretaryProgressFixture{}, nil, nil)
		_, err := service.Progress(
			t.Context(),
			auth.RequestMetadata{},
			domain.SecretaryProgressQuery{},
		)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	for _, store := range []domain.SecretaryStore{nil, &secretaryAPIFixture{}} {
		service := NewSecretaryService(workspacePrincipal{owner: "owner"}, store, nil, nil)
		_, err := service.Progress(
			t.Context(),
			auth.RequestMetadata{},
			domain.SecretaryProgressQuery{},
		)
		var apiErr apperr.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
	}
}

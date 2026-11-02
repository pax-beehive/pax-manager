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
	searchbackend "github.com/pax-beehive/pax-manager/internal/manager/search"
)

type groupedSources struct {
	workspaceSources
	metadata    domain.SearchGroupMetadata
	metadataErr error
	calls       int
}

func (s *groupedSources) ReadSearchGroup(
	_ context.Context,
	owner string,
	doc domain.SearchDocument,
) (domain.SearchGroupMetadata, error) {
	s.calls++
	if s.metadataErr != nil {
		return domain.SearchGroupMetadata{}, s.metadataErr
	}
	if owner != doc.OwnerUserID {
		return domain.SearchGroupMetadata{}, domain.ErrNotFound
	}
	if s.metadata.Key != "" {
		return s.metadata, nil
	}
	return domain.SearchGroupMetadata{
		Key:       domain.SearchDocumentGroupKey(doc),
		Title:     "Canonical current session name",
		AgentID:   doc.AgentID,
		SessionID: doc.SessionID,
	}, nil
}

type groupedBackend struct {
	workspaceBackend
	pages    []domain.SearchGroupPage
	queries  []domain.SearchQuery
	groupErr error
}

func (b *groupedBackend) SearchGroups(
	_ context.Context,
	query domain.SearchQuery,
) (domain.SearchGroupPage, error) {
	b.queries = append(b.queries, query)
	if b.groupErr != nil {
		return domain.SearchGroupPage{}, b.groupErr
	}
	if len(b.pages) == 0 {
		return domain.SearchGroupPage{}, nil
	}
	page := b.pages[0]
	b.pages = b.pages[1:]
	return page, nil
}

func groupedFixture() (*WorkspaceSearchService, *groupedSources, *groupedBackend, domain.SearchDocument) {
	doc := domain.SearchDocument{
		Kind:        "message",
		SourceID:    "m",
		OwnerUserID: "owner",
		AgentID:     "agent",
		SessionID:   "session",
		Body:        "port current PG text",
	}
	sources := &groupedSources{
		workspaceSources: workspaceSources{
			docs: map[string]domain.SearchDocument{doc.DocumentID(): doc},
		},
	}
	backend := &groupedBackend{
		pages: []domain.SearchGroupPage{
			{
				Groups: []domain.SearchGroup{
					{
						Key: domain.SearchDocumentGroupKey(doc),
						Matches: []domain.SearchDocument{
							{Kind: "message", SourceID: "m", Body: "untrusted ES text"},
						},
					},
				},
			},
		},
	}
	return NewWorkspaceSearchService(
		workspacePrincipal{owner: "owner"},
		sources,
		sources,
		backend,
		nil,
	), sources, backend, doc
}

func TestWorkspaceGroupedGivenCurrentMatchesThenUsesCanonicalTitleAndPGContent(t *testing.T) {
	service, sources, backend, doc := groupedFixture()
	for _, id := range []string{"m2", "m3", "m4"} {
		copy := doc
		copy.SourceID = id
		sources.docs[copy.DocumentID()] = copy
		backend.pages[0].Groups[0].Matches = append(backend.pages[0].Groups[0].Matches, copy)
	}
	page, err := service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "port", GroupBy: "session"},
	)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	group := page.Groups[0]
	assert.Equal(t, "Canonical current session name", group.Title)
	assert.Equal(t, "agent", group.AgentID)
	assert.Len(t, group.Matches, 3)
	assert.True(t, group.MatchesHasMore)
	assert.Equal(t, 1, sources.calls)
	assert.Equal(t, 20, backend.queries[0].Limit)
	assert.Contains(t, group.Matches[0].Snippet, "current PG text")
	assert.Empty(t, page.Hits)
}

func TestWorkspaceGroupedGivenStaleForeignOrReboundHitsThenFillsWithAuthorizedGroups(t *testing.T) {
	service, sources, backend, doc := groupedFixture()
	valid := backend.pages[0]
	backend.pages = []domain.SearchGroupPage{
		{
			Groups: []domain.SearchGroup{
				{
					Key:     "stale",
					Matches: []domain.SearchDocument{{Kind: "message", SourceID: "missing"}, doc},
				},
			},
			NextCursor: "next",
		},
		valid,
	}
	page, err := service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "port", GroupBy: "session", Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	require.Len(t, backend.queries, 2)
	assert.Equal(t, "next", backend.queries[1].Cursor)
	sources.metadata.Key = "wrong-current-group"
	backend.pages = []domain.SearchGroupPage{valid}
	page, err = service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "port", GroupBy: "session"},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Groups)
	sources.metadata.Key = ""
	sources.metadataErr = domain.ErrNotFound
	backend.pages = []domain.SearchGroupPage{valid}
	page, err = service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "port", GroupBy: "session"},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Groups)
}

func TestWorkspaceGroupedGivenMigrationErrorsOrInvalidModeThenReturnsUsefulErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{{searchbackend.ErrGroupMigrationRequired, http.StatusServiceUnavailable}, {searchbackend.ErrInvalidQuery, http.StatusBadRequest}, {errors.New("unavailable"), http.StatusServiceUnavailable}} {
		service, _, backend, _ := groupedFixture()
		backend.groupErr = test.err
		_, err := service.Search(
			t.Context(),
			auth.RequestMetadata{},
			domain.SearchQuery{Query: "port", GroupBy: "session"},
		)
		var httpErr apperr.Error
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, test.status, httpErr.Status)
	}
	service, _, _, _ := groupedFixture()
	for _, query := range []domain.SearchQuery{{Query: "port", GroupBy: "unsupported"}, {Query: "port", GroupBy: "session", Limit: 101}} {
		_, err := service.Search(t.Context(), auth.RequestMetadata{}, query)
		require.Error(t, err)
	}
	service.backend = &workspaceBackend{}
	_, err := service.Search(
		t.Context(),
		auth.RequestMetadata{},
		domain.SearchQuery{Query: "port", GroupBy: "session"},
	)
	require.Error(t, err)
}

func TestWorkspaceGroupedGivenRawExpansionThenRequiresExactCurrentGroup(t *testing.T) {
	service, _, backend, doc := groupedFixture()
	backend.page = domain.SearchPage{Hits: []domain.SearchDocument{doc}}
	query := domain.SearchQuery{Query: "port", GroupKey: domain.SearchDocumentGroupKey(doc)}
	page, err := service.Search(t.Context(), auth.RequestMetadata{}, query)
	require.NoError(t, err)
	assert.Len(t, page.Hits, 1)
	query.GroupKey = "another-group"
	page, err = service.Search(t.Context(), auth.RequestMetadata{}, query)
	require.NoError(t, err)
	assert.Empty(t, page.Hits)
}

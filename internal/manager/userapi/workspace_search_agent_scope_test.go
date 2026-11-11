package userapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestWorkspaceSearchGivenAgentScopeThenRechecksCurrentSourcesInEveryMode(t *testing.T) {
	for _, mode := range []string{"flat", "grouped", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			service, sources, backend, allowed := groupedFixture()
			other := allowed
			other.SourceID = "private-message"
			other.AgentID = "other-agent"
			other.Body = "port private content"
			sources.docs[other.DocumentID()] = other
			stale := other
			stale.AgentID = allowed.AgentID
			backend.page = domain.SearchPage{Hits: []domain.SearchDocument{allowed, stale}}
			backend.pages[0].Groups = append(backend.pages[0].Groups, domain.SearchGroup{
				Key: domain.SearchDocumentGroupKey(other), Matches: []domain.SearchDocument{stale},
			})
			query := domain.SearchQuery{
				Query: "port", AgentIDs: []string{allowed.AgentID}, SessionIDs: []string{allowed.SessionID},
			}
			switch mode {
			case "grouped":
				query.GroupBy = "session"
			case "expansion":
				query.GroupKey = domain.SearchDocumentGroupKey(allowed)
			}
			page, err := service.Search(t.Context(), auth.RequestMetadata{}, query)
			require.NoError(t, err)
			if mode == "grouped" {
				require.Len(t, page.Groups, 1)
				assert.Equal(t, allowed.AgentID, page.Groups[0].AgentID)
				require.Len(t, page.Groups[0].Matches, 1)
				assert.Equal(t, allowed.SourceID, page.Groups[0].Matches[0].Ref.ID)
				assert.Equal(t, query.AgentIDs, backend.queries[0].AgentIDs)
				assert.Equal(t, 1, sources.calls, "unauthorized group metadata must not be read")
			} else {
				require.Len(t, page.Hits, 1)
				assert.Equal(t, allowed.SourceID, page.Hits[0].Ref.ID)
				assert.Equal(t, query.AgentIDs, backend.query.AgentIDs)
			}
		})
	}
}

func TestWorkspaceSearchGivenOnlyForeignGroupThenCannotDrillIntoIt(t *testing.T) {
	service, _, backend, doc := groupedFixture()
	backend.page = domain.SearchPage{Hits: []domain.SearchDocument{doc}}
	query := domain.SearchQuery{
		Query: "port", AgentIDs: []string{"other-agent"}, GroupKey: domain.SearchDocumentGroupKey(doc),
	}
	page, err := service.Search(t.Context(), auth.RequestMetadata{}, query)
	require.NoError(t, err)
	assert.Empty(t, page.Hits)
}

func TestWorkspaceSearchGivenTooManyAgentsThenRejectsBeforeBackendCall(t *testing.T) {
	service, _, backend := workspaceFixture()
	_, err := service.Search(t.Context(), auth.RequestMetadata{}, domain.SearchQuery{
		Query: "port", AgentIDs: make([]string, 51),
	})
	requireWorkspaceStatus(t, err, http.StatusBadRequest)
	assert.Empty(t, backend.query)
}

package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	searchbackend "github.com/pax-beehive/pax-manager/internal/manager/search"
)

type WorkspaceSearchGroup struct {
	domain.SearchGroupMetadata
	Matches        []WorkspaceSearchHit `json:"matches"`
	MatchesHasMore bool                 `json:"matches_has_more"`
}

func (s *WorkspaceSearchService) readSearchGroup(
	ctx context.Context,
	owner string,
	doc domain.SearchDocument,
) (domain.SearchGroupMetadata, error) {
	store, ok := s.sources.(domain.SearchGroupSourceStore)
	if !ok {
		return domain.SearchGroupMetadata{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Session grouping metadata is unavailable",
		)
	}
	group, err := store.ReadSearchGroup(ctx, owner, doc)
	return group, searchSourceError(err)
}

func (s *WorkspaceSearchService) searchGroups(
	ctx context.Context,
	owner string,
	query domain.SearchQuery,
) (WorkspaceSearchPage, error) {
	backend, ok := s.backend.(domain.GroupedSearchBackend)
	if !ok || s.sources == nil {
		return WorkspaceSearchPage{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Grouped search is unavailable",
		)
	}
	query.OwnerUserID = owner
	if query.Limit == 0 {
		query.Limit = 20
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result := WorkspaceSearchPage{
		Hits:   []WorkspaceSearchHit{},
		Groups: make([]WorkspaceSearchGroup, 0, query.Limit),
	}
	seen := map[string]bool{}
	for scanned := 0; scanned < query.Limit+100; {
		request := query
		request.Limit = min(query.Limit-len(result.Groups), query.Limit+100-scanned)
		page, err := backend.SearchGroups(ctx, request)
		if errors.Is(err, searchbackend.ErrInvalidQuery) {
			return WorkspaceSearchPage{}, searchHTTPError(
				http.StatusBadRequest,
				"Invalid grouped search cursor or query",
			)
		}
		if errors.Is(err, searchbackend.ErrGroupMigrationRequired) {
			return WorkspaceSearchPage{}, searchHTTPError(
				http.StatusServiceUnavailable,
				"Grouped search requires the search index migration to finish",
			)
		}
		if err != nil {
			return WorkspaceSearchPage{}, searchHTTPError(
				http.StatusServiceUnavailable,
				"Grouped search request failed",
			)
		}
		for _, candidate := range page.Groups {
			scanned++
			group, readErr := s.currentSearchGroup(ctx, owner, query, candidate)
			if readErr != nil {
				return WorkspaceSearchPage{}, readErr
			}
			if len(group.Matches) > 0 && !seen[group.Key] {
				result.Groups = append(result.Groups, group)
				seen[group.Key] = true
			}
		}
		result.NextCursor, result.PaginationLimited = page.NextCursor, page.PaginationLimited
		if len(result.Groups) >= query.Limit || page.NextCursor == "" || len(page.Groups) == 0 {
			break
		}
		query.Cursor = page.NextCursor
	}
	return result, nil
}

func (s *WorkspaceSearchService) currentSearchGroup(
	ctx context.Context,
	owner string,
	query domain.SearchQuery,
	candidate domain.SearchGroup,
) (WorkspaceSearchGroup, error) {
	group := WorkspaceSearchGroup{
		Matches:        []WorkspaceSearchHit{},
		MatchesHasMore: candidate.HasMore,
	}
	seen := map[string]bool{}
	for _, hit := range candidate.Matches {
		current, err := s.currentSearchDocument(ctx, owner, query, hit)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return group, searchSourceError(err)
		}
		if !searchMatchesCurrent(current, owner, query) {
			continue
		}
		key := domain.SearchDocumentGroupKey(current)
		if key != candidate.Key || query.GroupKey != "" && key != query.GroupKey {
			continue
		}
		if group.Key == "" {
			metadata, readErr := s.readSearchGroup(ctx, owner, current)
			if errors.Is(readErr, domain.ErrNotFound) {
				continue
			}
			if readErr != nil {
				return group, readErr
			}
			if metadata.Key != candidate.Key ||
				(len(query.NodeIDs) > 0 && !searchFilterContains(query.NodeIDs, metadata.NodeID)) {
				continue
			}
			group.SearchGroupMetadata = metadata
		}
		if seen[current.DocumentID()] {
			continue
		}
		seen[current.DocumentID()] = true
		if len(group.Matches) >= 3 {
			group.MatchesHasMore = true
			continue
		}
		text := current.Body
		if searchTermIndex(strings.ToLower(text), domain.SearchTerms(query.Query)) < 0 {
			text = current.Title
		}
		group.Matches = append(
			group.Matches,
			WorkspaceSearchHit{
				Ref:    documentRef(current),
				Title:  current.Title,
				NodeID: current.NodeID,
				Snippet: searchSnippet(
					text,
					query.Query,
				),
				AgentID:   current.AgentID,
				SessionID: current.SessionID,
			},
		)
	}
	return group, nil
}

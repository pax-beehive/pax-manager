package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *WorkspaceSearchService) ListSummaryRevisions(
	ctx context.Context,
	meta auth.RequestMetadata,
	id string,
	query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	if strings.TrimSpace(id) == "" || query.Limit < 0 || query.Limit > 100 {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary revision query",
		)
	}
	store, ok := s.summaries.(domain.SummaryRevisionStore)
	if !ok {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Summary storage is unavailable",
		)
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	page, err := store.ListSummaryRevisions(ctx, owner, id, query)
	if errors.Is(err, domain.ErrInvalidCursor) {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary revision cursor",
		)
	}
	return page, searchSourceError(err)
}

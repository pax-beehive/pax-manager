package userapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *WorkspaceSearchService) ListSecretarySummaries(
	ctx context.Context, meta auth.RequestMetadata, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	if query.Limit < 0 || query.Limit > 100 {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary page query",
		)
	}
	store, ok := s.summaries.(domain.SummaryRecentBrowserStore)
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
	page, err := store.ListRecentSummaries(ctx, owner, query)
	if errors.Is(err, domain.ErrInvalidCursor) {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary page cursor",
		)
	}
	return page, searchSourceError(err)
}

package userapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type DeveloperSummaryRead struct {
	Summary domain.SummaryVersion `json:"summary"`
}

func (s *WorkspaceSearchService) developerOwner(
	ctx context.Context,
	meta auth.RequestMetadata,
) (string, error) {
	if s.principal == nil {
		return "", domain.ErrUnauthorized
	}
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return "", err
	}
	if principal.User.UserID == "" {
		return "", domain.ErrUnauthorized
	}
	if !principal.IsAdmin {
		return "", searchHTTPError(http.StatusForbidden, "Administrator access is required")
	}
	return principal.User.UserID, nil
}

func (s *WorkspaceSearchService) ListDeveloperSummaries(
	ctx context.Context,
	meta auth.RequestMetadata,
	query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	owner, err := s.developerOwner(ctx, meta)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	if query.Limit < 0 || query.Limit > 100 {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary page query",
		)
	}
	store, ok := s.summaries.(domain.SummaryBrowserStore)
	if !ok {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Summary storage is unavailable",
		)
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	page, err := store.ListCurrentSummaries(ctx, owner, query)
	if errors.Is(err, domain.ErrInvalidCursor) {
		return domain.SummaryListPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary page cursor",
		)
	}
	return page, searchSourceError(err)
}

func (s *WorkspaceSearchService) ReadDeveloperSummary(
	ctx context.Context,
	meta auth.RequestMetadata,
	id string,
	revision int64,
) (DeveloperSummaryRead, error) {
	owner, err := s.developerOwner(ctx, meta)
	if err != nil {
		return DeveloperSummaryRead{}, err
	}
	if strings.TrimSpace(id) == "" || revision < 0 {
		return DeveloperSummaryRead{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid summary identity or revision",
		)
	}
	if s.summaries == nil {
		return DeveloperSummaryRead{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Summary storage is unavailable",
		)
	}
	version, err := s.summaries.GetSummary(ctx, owner, id, revision)
	return DeveloperSummaryRead{Summary: version}, searchSourceError(err)
}

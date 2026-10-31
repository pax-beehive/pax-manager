package userapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *SecretaryService) Progress(
	ctx context.Context,
	meta auth.RequestMetadata,
	query domain.SecretaryProgressQuery,
) (domain.SecretaryProgressPage, error) {
	principal, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 || query.Limit > 100 || !validSecretaryProgressCursor(query.Cursor) {
		return domain.SecretaryProgressPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid Secretary progress cursor or limit",
		)
	}
	progress, ok := s.store.(domain.SecretaryProgressStore)
	if !ok {
		return domain.SecretaryProgressPage{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary progress is unavailable",
		)
	}
	page, err := progress.ReadSecretaryProgress(ctx, principal.User.UserID, query)
	if errors.Is(err, domain.ErrInvalidCursor) {
		return domain.SecretaryProgressPage{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid Secretary progress cursor or limit",
		)
	}
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	if page.Sessions == nil {
		page.Sessions = []domain.SecretarySessionProgress{}
	}
	return page, nil
}

func validSecretaryProgressCursor(cursor string) bool {
	if len(cursor) > 2048 {
		return false
	}
	for _, character := range cursor {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}

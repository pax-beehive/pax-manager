package storage

import (
	"context"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) ListCurrentSummaries(
	_ context.Context, owner string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	after, limit, err := summaryListQuery(owner, query)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0)
	for id, versions := range s.secretaryLocked().summaries {
		if len(versions) > 0 && versions[len(versions)-1].OwnerUserID == owner && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	page := domain.SummaryListPage{Items: make([]domain.SummaryListItem, 0, min(limit, len(ids)))}
	if len(ids) > limit {
		page.NextCursor = summaryNextCursor(owner, ids[limit-1])
		ids = ids[:limit]
	}
	for _, id := range ids {
		summary, readErr := s.getMemorySummaryLocked(owner, id, 0, map[domain.SummaryRef]bool{})
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return domain.SummaryListPage{}, readErr
		}
		page.Items = append(page.Items, summaryListItem(summary))
	}
	return page, nil
}

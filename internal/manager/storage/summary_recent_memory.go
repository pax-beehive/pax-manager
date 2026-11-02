package storage

import (
	"context"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) ListRecentSummaries(
	_ context.Context, owner string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	cursor, limit, err := summaryRecentQuery(owner, query)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]summaryRecentRef, 0)
	for _, versions := range s.secretaryLocked().summaries {
		if len(versions) == 0 {
			continue
		}
		head := versions[len(versions)-1]
		ref := summaryRecentRef{
			SummaryRef: domain.SummaryRef{SummaryID: head.SummaryID, Revision: head.Revision},
			CreatedAt:  head.CreatedAt,
		}
		if head.OwnerUserID == owner && summaryRecentAfter(ref, cursor) {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].CreatedAt.Equal(refs[j].CreatedAt) {
			return refs[i].SummaryID > refs[j].SummaryID
		}
		return refs[i].CreatedAt.After(refs[j].CreatedAt)
	})
	page := domain.SummaryListPage{Items: make([]domain.SummaryListItem, 0, min(limit, len(refs)))}
	if len(refs) > limit {
		page.NextCursor = summaryRecentNextCursor(owner, refs[limit-1])
		refs = refs[:limit]
	}
	for _, ref := range refs {
		summary, readErr := s.getMemorySummaryLocked(
			owner,
			ref.SummaryID,
			ref.Revision,
			map[domain.SummaryRef]bool{},
		)
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

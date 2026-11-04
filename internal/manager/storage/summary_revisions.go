package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func summaryRevisionQuery(owner, id string, query domain.SummaryListQuery) (int64, int, error) {
	if owner == "" {
		return 0, 0, ErrUnauthorized
	}
	if id == "" {
		return 0, 0, ErrNotFound
	}
	after, limit, err := summaryListQuery(owner+":"+id, query)
	if err != nil {
		return 0, 0, err
	}
	if after == "" {
		return math.MaxInt64, limit, nil
	}
	revision, err := strconv.ParseInt(after, 10, 64)
	if err != nil || revision <= 0 {
		return 0, 0, domain.ErrInvalidCursor
	}
	return revision, limit, nil
}

func (s *MemoryStore) ListSummaryRevisions(
	_ context.Context, owner, id string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	after, limit, err := summaryRevisionQuery(owner, id, query)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.secretaryLocked().summaries[id]
	if len(versions) == 0 || versions[0].OwnerUserID != owner {
		return domain.SummaryListPage{}, ErrNotFound
	}
	page := domain.SummaryListPage{Items: []domain.SummaryListItem{}}
	scanned := 0
	for revision := min(int64(len(versions)), after-1); revision > 0; revision-- {
		if scanned == limit {
			page.NextCursor = summaryNextCursor(owner+":"+id, strconv.FormatInt(revision+1, 10))
			break
		}
		scanned++
		version, readErr := s.getMemorySummaryLocked(
			owner,
			id,
			revision,
			map[domain.SummaryRef]bool{},
		)
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return domain.SummaryListPage{}, readErr
		}
		page.Items = append(page.Items, summaryListItem(version))
	}
	return page, nil
}

func (s *PostgresStore) ListSummaryRevisions(
	ctx context.Context, owner, id string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	after, limit, err := summaryRevisionQuery(owner, id, query)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT revision FROM summary_versions WHERE owner_user_id=$1
 AND summary_id=$2 AND revision<$3 ORDER BY revision DESC LIMIT $4`, owner, id, after, limit+1)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	var revisions []int64
	for rows.Next() {
		var revision int64
		if err = rows.Scan(&revision); err != nil {
			_ = rows.Close()
			return domain.SummaryListPage{}, err
		}
		revisions = append(revisions, revision)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return domain.SummaryListPage{}, err
	}
	_ = rows.Close()
	page := domain.SummaryListPage{Items: []domain.SummaryListItem{}}
	if len(revisions) > limit {
		page.NextCursor = summaryNextCursor(owner+":"+id, strconv.FormatInt(revisions[limit-1], 10))
		revisions = revisions[:limit]
	}
	for _, revision := range revisions {
		version, readErr := getSummaryTx(ctx, tx, owner, id, revision)
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return domain.SummaryListPage{}, readErr
		}
		page.Items = append(page.Items, summaryListItem(version))
	}
	return page, tx.Commit()
}

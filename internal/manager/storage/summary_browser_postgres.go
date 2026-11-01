package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) ListCurrentSummaries(
	ctx context.Context, owner string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	after, limit, err := summaryListQuery(owner, query)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	refs, err := listCurrentSummaryRefs(ctx, tx, owner, after, limit+1)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	page := domain.SummaryListPage{Items: make([]domain.SummaryListItem, 0, min(limit, len(refs)))}
	if len(refs) > limit {
		page.NextCursor = summaryNextCursor(owner, refs[limit-1].SummaryID)
		refs = refs[:limit]
	}
	for _, ref := range refs {
		summary, readErr := getSummaryTx(ctx, tx, owner, ref.SummaryID, ref.Revision)
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return domain.SummaryListPage{}, readErr
		}
		page.Items = append(page.Items, summaryListItem(summary))
	}
	return page, tx.Commit()
}

func listCurrentSummaryRefs(
	ctx context.Context,
	tx *sql.Tx,
	owner, after string,
	limit int,
) ([]domain.SummaryRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT summary_id, MAX(revision) FROM summary_versions
        WHERE owner_user_id = $1 AND summary_id > $2
        GROUP BY summary_id ORDER BY summary_id LIMIT $3`, owner, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	refs := make([]domain.SummaryRef, 0, limit)
	for rows.Next() {
		var ref domain.SummaryRef
		if err = rows.Scan(&ref.SummaryID, &ref.Revision); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

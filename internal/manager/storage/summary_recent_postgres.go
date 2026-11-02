package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) ListRecentSummaries(
	ctx context.Context, owner string, query domain.SummaryListQuery,
) (domain.SummaryListPage, error) {
	cursor, limit, err := summaryRecentQuery(owner, query)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	refs, err := listRecentSummaryRefs(ctx, tx, owner, cursor, limit+1)
	if err != nil {
		return domain.SummaryListPage{}, err
	}
	page := domain.SummaryListPage{Items: make([]domain.SummaryListItem, 0, min(limit, len(refs)))}
	if len(refs) > limit {
		page.NextCursor = summaryRecentNextCursor(owner, refs[limit-1])
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

func listRecentSummaryRefs(
	ctx context.Context, tx *sql.Tx, owner string, cursor summaryRecentCursor, limit int,
) ([]summaryRecentRef, error) {
	rows, err := tx.QueryContext(ctx, `WITH current_summaries AS (
        SELECT DISTINCT ON (summary_id) summary_id, revision, created_at
        FROM summary_versions WHERE owner_user_id = $1
        ORDER BY summary_id, revision DESC)
        SELECT summary_id, revision, created_at FROM current_summaries
        WHERE $2::text = '' OR (created_at, summary_id) < ($3::timestamptz, $2::text)
        ORDER BY created_at DESC, summary_id DESC LIMIT $4`, owner, cursor.After, cursor.Time, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	refs := make([]summaryRecentRef, 0, limit)
	for rows.Next() {
		var ref summaryRecentRef
		if err = rows.Scan(&ref.SummaryID, &ref.Revision, &ref.CreatedAt); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

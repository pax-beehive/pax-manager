package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Batch current canonical visibility checks without a round trip for every leaf.
func validateSearchMessageAccess(
	ctx context.Context, tx *sql.Tx, owner string, ids []string,
) error {
	if len(ids) == 0 {
		return nil
	}
	expected := make(map[string]bool, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if !expected[id] {
			expected[id] = true
			unique = append(unique, id)
		}
	}
	encoded, _ := json.Marshal(unique)
	rows, err := tx.QueryContext(ctx, `SELECT to_jsonb(m), a.owner_user_id,
 COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.part_index)
 FROM message_parts p WHERE p.message_id=m.message_id),'[]'::jsonb), 0`+
		searchMessageVisibilityFromSQL+
		` WHERE m.message_id IN (SELECT jsonb_array_elements_text($1::jsonb))`+
		searchMessageVisibilityConditionsSQL, string(encoded))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	visible := make(map[string]bool, len(expected))
	for rows.Next() {
		doc, readErr := scanCanonicalSearchMessage(rows, 0)
		if readErr != nil {
			return readErr
		}
		if doc.OwnerUserID != owner || !expected[doc.SourceID] {
			return ErrNotFound
		}
		visible[doc.SourceID] = true
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(visible) != len(expected) {
		return domain.ErrNotFound
	}
	return rows.Close()
}

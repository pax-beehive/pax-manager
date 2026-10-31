package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryProgressStore = (*PostgresStore)(nil)

// Reuse the canonical source visibility predicate, including replacement display rows.
var secretaryProgressSQL = `WITH visible AS (
 SELECT m.message_id,s.id source_id,s.agent_id,s.session_id,
 COALESCE(s.custom_session_name,s.session_name,'') title` + strings.Replace(
	searchMessageVisibilitySQL,
	"WHERE m.message_id = $1",
	"WHERE a.owner_user_id = $1 AND s.id IS NOT NULL",
	1,
) + ` AND NOT EXISTS (SELECT 1 FROM secretary_internal_sessions i
 WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
 ) SELECT v.source_id::text,v.agent_id,v.session_id,v.title,j.completed_at,to_jsonb(m),
 COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.part_index) FROM message_parts p WHERE p.message_id=m.message_id),'[]'::jsonb),
 COALESCE(state.processed_hash,''),COALESCE(state.processed_canonicalization_version,''),COALESCE(state.processed_owner_user_id,'')
 FROM visible v JOIN messages m ON m.message_id=v.message_id
 LEFT JOIN secretary_jobs j ON j.source_id=v.source_id AND j.owner_user_id=$1
 LEFT JOIN secretary_message_state state ON state.source_id=j.source_id AND state.message_id=m.message_id
 ORDER BY v.source_id,m.id`

func (s *PostgresStore) ReadSecretaryProgress(
	ctx context.Context,
	owner string,
	query domain.SecretaryProgressQuery,
) (domain.SecretaryProgressPage, error) {
	after, limit, err := secretaryProgressQuery(owner, query)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var asOf time.Time
	if err = tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&asOf); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	rows, err := tx.QueryContext(ctx, secretaryProgressSQL, owner)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	progress, err := scanSecretaryProgress(rows, owner)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	return progress.page(owner, after, limit, asOf), tx.Commit()
}

func scanSecretaryProgress(rows *sql.Rows, owner string) (secretaryProgressAccumulator, error) {
	defer func() { _ = rows.Close() }()
	progress := secretaryProgressAccumulator{}
	for rows.Next() {
		var row domain.SecretarySessionProgress
		var rawMessage, rawParts []byte
		var processedHash, processedCanonicalization, processedOwner string
		err := rows.Scan(
			&row.SourceID,
			&row.AgentID,
			&row.SessionID,
			&row.Title,
			&row.LastCompletedAt,
			&rawMessage,
			&rawParts,
			&processedHash,
			&processedCanonicalization,
			&processedOwner,
		)
		if err != nil {
			return nil, err
		}
		var message domain.MessageWithParts
		if err = json.Unmarshal(rawMessage, &message.Message); err != nil {
			return nil, fmt.Errorf("decode secretary progress message: %w", err)
		}
		if err = json.Unmarshal(rawParts, &message.Parts); err != nil {
			return nil, fmt.Errorf("decode secretary progress parts: %w", err)
		}
		doc, err := domain.CanonicalSearchMessage(message)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		completed := processedOwner == owner && processedHash == doc.ContentHash &&
			processedCanonicalization == doc.CanonicalizationVersion
		progress.add(row, message.TurnID, completed)
	}
	return progress, rows.Err()
}

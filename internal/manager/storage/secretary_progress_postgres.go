package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryProgressStore = (*PostgresStore)(nil)

// Reuse the canonical source visibility predicate, including replacement display rows.
var secretaryProgressSQL = buildSecretaryProgressSQL()

// These are precisely the runes removed by strings.TrimSpace for UTF-8 text.
const secretaryProgressWhitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

func buildSecretaryProgressSQL() string {
	kinds := domain.SearchVisibleMessageTypes()
	for i, kind := range kinds {
		kinds[i] = "'" + strings.ReplaceAll(kind, "'", "''") + "'"
	}
	// Progress requires an existing session, so both session IDs are nonnull.
	// Equality lets PostgreSQL hash replacements by agent and session together.
	visibility := strings.ReplaceAll(searchMessageVisibilitySQL,
		"display.session_id IS NOT DISTINCT FROM m.session_id", "display.session_id = m.session_id")
	return `WITH visible AS (
 SELECT m.message_id,s.id source_id,s.agent_id,s.session_id,
 COALESCE(s.custom_session_name,s.session_name,'') title,m.role,m.message_type,m.raw_json,m.turn_id` + strings.Replace(
		visibility,
		"WHERE m.message_id = $1",
		"WHERE a.owner_user_id = $1 AND s.id IS NOT NULL AND m.role IN ('user','assistant') AND COALESCE(m.message_type,'') IN ("+strings.Join(
			kinds,
			",",
		)+")",
		1,
	) + ` AND NOT EXISTS (SELECT 1 FROM secretary_internal_sessions i
 WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
 ) SELECT v.source_id::text,v.agent_id,v.session_id,v.title,j.completed_at,
 jsonb_build_object('message_id',v.message_id,'role',v.role,'message_type',v.message_type,'raw_json',v.raw_json,'turn_id',v.turn_id),
 'sha256:'||encode(sha256(convert_to(parts.body,'UTF8')),'hex'),
 COALESCE(state.processed_hash,''),COALESCE(state.processed_canonicalization_version,''),COALESCE(state.processed_owner_user_id,'')
 FROM visible v
 JOIN LATERAL (SELECT string_agg(CASE WHEN p.part_type='text' THEN COALESCE(p.text,'') ELSE '' END,'' ORDER BY p.part_index) body
 FROM message_parts p WHERE p.message_id=v.message_id
 HAVING bool_and(p.part_index>=0) AND count(*)=count(DISTINCT p.part_index)) parts ON btrim(parts.body,$2)<>''
 LEFT JOIN secretary_jobs j ON j.source_id=v.source_id AND j.owner_user_id=$1
 LEFT JOIN secretary_message_state state ON state.source_id=j.source_id AND state.message_id=v.message_id`
}

func (s *PostgresStore) ReadSecretaryProgress(
	ctx context.Context,
	owner string,
	query domain.SecretaryProgressQuery,
) (domain.SecretaryProgressPage, error) {
	after, limit, err := secretaryProgressQuery(owner, query)
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='8s'`); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	var asOf time.Time
	if err = tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&asOf); err != nil {
		return domain.SecretaryProgressPage{}, err
	}
	rows, err := tx.QueryContext(ctx, secretaryProgressSQL, owner, secretaryProgressWhitespace)
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
		var rawMessage []byte
		var currentHash string
		var processedHash, processedCanonicalization, processedOwner string
		err := rows.Scan(
			&row.SourceID,
			&row.AgentID,
			&row.SessionID,
			&row.Title,
			&row.LastCompletedAt,
			&rawMessage,
			&currentHash,
			&processedHash,
			&processedCanonicalization,
			&processedOwner,
		)
		if err != nil {
			return nil, err
		}
		var message domain.Message
		if err = json.Unmarshal(rawMessage, &message); err != nil {
			return nil, fmt.Errorf("decode secretary progress message: %w", err)
		}
		if !domain.SearchVisibleMessageMetadata(message) {
			continue
		}
		completed := processedOwner == owner && processedHash == currentHash &&
			processedCanonicalization == domain.SearchCanonicalizationVersion
		progress.add(row, message.TurnID, completed)
	}
	return progress, rows.Err()
}

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryProgressStore = (*PostgresStore)(nil)

// Reuse the canonical source visibility predicate, including replacement display rows.
var secretaryProgressSQL = buildSecretaryProgressSQL()

// These are precisely the runes removed by strings.TrimSpace for UTF-8 text.
const secretaryProgressWhitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

func buildSecretaryProgressSQL() string {
	return buildSecretaryProgressBatchSQL(false)
}

func buildSecretaryProgressBatchSQL(batch bool) string {
	sessionScope, messageScope, identity := "", "", ""
	if batch {
		sessionScope = ` AND EXISTS (SELECT 1 FROM messages selected WHERE selected.agent_id=s.agent_id AND selected.session_id=s.session_id AND selected.message_id IN (SELECT jsonb_array_elements_text($3::jsonb)))`
		messageScope = ` AND m.message_id IN (SELECT jsonb_array_elements_text($3::jsonb))`
		identity = "v.message_id,"
	}
	// Classify each display once across the owner's sessions before counting sources.
	prefix := `WITH target_sessions AS MATERIALIZED (
 SELECT DISTINCT s.agent_id,s.session_id FROM agent_sessions s
 JOIN agents a ON a.agent_id=s.agent_id
 WHERE a.owner_user_id=$1 AND a.deleted_at IS NULL AND s.archived_at IS NULL` + sessionScope + `
), display_rows AS MATERIALIZED (
 SELECT display.agent_id,display.session_id,display.parent_message_id,
 CASE WHEN jsonb_typeof(display.raw_json->'replaces_message_ids')='array' THEN
 CASE WHEN display.raw_json->'replaces_message_ids'<>'[]'::jsonb AND NOT EXISTS (
 SELECT 1 FROM jsonb_array_elements(display.raw_json->'replaces_message_ids') item
 WHERE jsonb_typeof(item) NOT IN ('string','null'))
 THEN display.raw_json->'replaces_message_ids' END END replacement_ids
 FROM target_sessions target JOIN messages display
 ON display.agent_id=target.agent_id AND display.session_id=target.session_id
 WHERE display.message_type IN ('pax:invocation','pax:invocation_pending','pax:artifact')
), replacement_targets AS MATERIALIZED (
 SELECT display.agent_id,display.session_id,item.value #>> '{}' message_id
 FROM display_rows display CROSS JOIN LATERAL jsonb_array_elements(
 CASE WHEN display.replacement_ids IS NOT NULL THEN display.replacement_ids
 ELSE jsonb_build_array(display.parent_message_id) END) item(value)
 WHERE jsonb_typeof(item.value)='string'
), `
	visibility := searchMessageVisibilityFromSQL + ` WHERE m.message_id = $1` +
		searchMessageSourceVisibilitySQL + ` AND NOT EXISTS (
 SELECT 1 FROM replacement_targets replacement
 WHERE replacement.agent_id=m.agent_id AND replacement.session_id=m.session_id
 AND replacement.message_id=m.message_id)`
	return prefix + strings.TrimPrefix(
		buildSecretaryProgressScopeSQL(messageScope, identity, visibility),
		"WITH ",
	)
}

func buildSecretaryProgressScopeSQL(scope, identityColumn, visibility string) string {
	kinds := domain.SearchVisibleMessageTypes()
	for i, kind := range kinds {
		kinds[i] = "'" + strings.ReplaceAll(kind, "'", "''") + "'"
	}
	// Progress requires an existing session, so both session IDs are nonnull.
	// Equality lets PostgreSQL hash replacements by agent and session together.
	visibility = strings.ReplaceAll(visibility,
		"display.session_id IS NOT DISTINCT FROM m.session_id", "display.session_id = m.session_id")
	return `WITH visible AS (
 SELECT m.message_id,s.id source_id,s.agent_id,s.session_id,
 COALESCE(s.custom_session_name,s.session_name,'') title,m.role,m.message_type,m.raw_json,m.turn_id` + strings.Replace(
		visibility,
		"WHERE m.message_id = $1",
		"WHERE a.owner_user_id = $1 AND s.id IS NOT NULL AND m.role IN ('user','assistant') AND COALESCE(m.message_type,'') IN ("+strings.Join(
			kinds,
			",",
		)+")"+scope,
		1,
	) + ` AND NOT EXISTS (SELECT 1 FROM secretary_internal_sessions i
 WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
 ) SELECT ` + identityColumn + `v.source_id::text,v.agent_id,v.session_id,v.title,j.completed_at,
 jsonb_build_object('message_id',v.message_id,'role',v.role,'message_type',v.message_type,'raw_json',v.raw_json,'turn_id',v.turn_id) message,
 'sha256:'||encode(sha256(convert_to(parts.body,'UTF8')),'hex') current_hash,
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
	page, err := s.readSecretaryProgressProjection(ctx, owner, after, limit)
	var state interface{ SQLState() string }
	if errors.As(err, &state) && (state.SQLState() == "40001" || state.SQLState() == "40P01") {
		err = domain.ErrSecretaryProgressPreparing
	}
	return page, err
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

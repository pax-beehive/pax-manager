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

// Hash only dirty message identities and invalidated dependencies, not session history.
var secretaryPendingSQL = `WITH RECURSIVE pending_jobs AS MATERIALIZED (
 SELECT j.* FROM secretary_jobs j
 JOIN agents a ON a.agent_id=j.source_agent_id AND a.owner_user_id=j.owner_user_id
 JOIN agent_sessions s ON s.id=j.source_id AND s.agent_id=j.source_agent_id AND s.session_id=j.source_session_id
 LEFT JOIN nodes an ON an.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE j.owner_user_id=$1 AND (j.desired_revision>j.processed_revision OR
 EXISTS(SELECT 1 FROM secretary_operations op WHERE op.owner_user_id=j.owner_user_id
 AND op.source_id=j.source_id AND op.status IN ('queued','running','needs_attention')))
 AND a.deleted_at IS NULL AND an.deleted_at IS NULL AND sn.deleted_at IS NULL AND s.archived_at IS NULL
 AND COALESCE(s.source,'')<>'secretary_internal' AND NOT starts_with(s.session_id,'sess_secretary_')
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
), dependencies(source_id,message_id,summary_id,revision) AS (
 SELECT p.source_id,i.message_id,n.id,n.current_revision FROM pending_jobs p
 JOIN knowledge_node_invalidations i ON i.origin_source_id=p.source_id::text
 JOIN knowledge_nodes n ON n.id=i.node_id AND n.owner_user_id=$1
 WHERE i.acknowledged_operation_id=''
 UNION
 SELECT d.source_id,d.message_id,src.child_summary_id,src.child_revision FROM dependencies d
 JOIN summary_sources src ON src.summary_id=d.summary_id AND src.revision=d.revision
 WHERE src.source_kind='summary'
), pending_candidates AS MATERIALIZED (
 SELECT j.source_id,state.message_id,j.source_agent_id expected_agent,j.source_session_id expected_session,
 CASE WHEN state.processed_owner_user_id=$1 THEN state.processed_hash ELSE '' END expected_hash,
 state.processed_canonicalization_version expected_canonicalization
 FROM pending_jobs j JOIN secretary_message_state state ON state.source_id=j.source_id
 AND state.change_revision>j.processed_revision
 AND (state.processed_owner_user_id<>$1 OR state.processed_change_revision<state.change_revision)
 UNION
 SELECT d.source_id,src.message_id,'','',src.content_hash,src.canonicalization_version
 FROM dependencies d JOIN summary_sources src ON src.summary_id=d.summary_id AND src.revision=d.revision
 AND src.source_kind='message' AND src.message_id=d.message_id
), candidate_messages AS MATERIALIZED (
 SELECT m.message_id,m.agent_id,m.session_id FROM messages m
 JOIN (SELECT DISTINCT message_id FROM pending_candidates) c USING(message_id)
 WHERE m.role IN ('user','assistant') AND COALESCE(m.message_type,'') IN (` + secretaryPendingKindsSQL() + `)
), display_rows AS MATERIALIZED (
 SELECT display.agent_id,display.session_id,
 CASE WHEN jsonb_typeof(display.raw_json->'replaces_message_ids')='array' THEN
 CASE WHEN display.raw_json->'replaces_message_ids'<>'[]'::jsonb AND NOT EXISTS(
 SELECT 1 FROM jsonb_array_elements(display.raw_json->'replaces_message_ids') item
 WHERE jsonb_typeof(item) NOT IN ('string','null'))
 THEN display.raw_json->'replaces_message_ids' ELSE jsonb_build_array(display.parent_message_id) END
 ELSE jsonb_build_array(display.parent_message_id) END replaced_ids
 FROM (SELECT DISTINCT agent_id,session_id FROM candidate_messages) target
 JOIN messages display ON display.agent_id=target.agent_id AND display.session_id=target.session_id
 AND display.message_type IN ('pax:invocation','pax:invocation_pending','pax:artifact')
), replaced_candidates AS MATERIALIZED (
 SELECT DISTINCT display.agent_id,display.session_id,item #>> '{}' message_id FROM display_rows display
 CROSS JOIN LATERAL jsonb_array_elements(display.replaced_ids) item
 WHERE jsonb_typeof(item)='string' AND display.agent_id IS NOT NULL AND display.session_id IS NOT NULL
), current_messages AS MATERIALIZED (` + buildSecretaryProgressScopeSQL(
	` AND EXISTS(SELECT 1 FROM candidate_messages candidate WHERE candidate.message_id=m.message_id)`,
	"v.message_id canonical_message_id,",
	searchMessageVisibilityFromSQL+` WHERE m.message_id = $1`+searchMessageSourceVisibilitySQL+
		` AND (m.agent_id,m.session_id,m.message_id) NOT IN (SELECT agent_id,session_id,message_id FROM replaced_candidates)`,
) + `)
 SELECT c.source_id::text,c.expected_agent,c.expected_session,COALESCE(m.agent_id,''),COALESCE(m.session_id,''),
 COALESCE(m.message,'{}'::jsonb),COALESCE(m.current_hash,''),c.expected_hash,c.expected_canonicalization,FALSE
 FROM pending_candidates c LEFT JOIN current_messages m ON m.canonical_message_id=c.message_id
 UNION ALL
 SELECT j.source_id::text,'','','','','{}'::jsonb,'','','',TRUE FROM pending_jobs j
 WHERE EXISTS(SELECT 1 FROM secretary_operations op WHERE op.owner_user_id=j.owner_user_id
 AND op.source_id=j.source_id AND op.status IN ('queued','running','needs_attention')) OR
 EXISTS(SELECT 1 FROM secretary_batches b WHERE b.source_id=j.source_id AND b.owner_user_id=j.owner_user_id
 AND b.settings_revision=$3 AND b.retry_epoch=j.retry_epoch
 AND b.state IN ('pending','running','output_received','unknown','blocked'))`

func secretaryPendingKindsSQL() string {
	kinds := domain.SearchVisibleMessageTypes()
	for i, kind := range kinds {
		kinds[i] = "'" + strings.ReplaceAll(kind, "'", "''") + "'"
	}
	return strings.Join(kinds, ",")
}

func (s *PostgresStore) secretaryPendingCount(
	ctx context.Context,
	owner string,
	settings int64,
) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(
		ctx,
		secretaryPendingSQL,
		owner,
		secretaryProgressWhitespace,
		settings,
	)
	if err != nil {
		return 0, err
	}
	return scanSecretaryPending(rows)
}

func scanSecretaryPending(rows *sql.Rows) (int, error) {
	defer func() { _ = rows.Close() }()
	pending := map[string]bool{}
	for rows.Next() {
		var source, expectedAgent, expectedSession, agent, session, hash, expectedHash, canonical string
		var raw []byte
		var execution bool
		if err := rows.Scan(&source, &expectedAgent, &expectedSession, &agent, &session,
			&raw, &hash, &expectedHash, &canonical, &execution); err != nil {
			return 0, err
		}
		if pending[source] {
			continue
		}
		if execution {
			pending[source] = true
			continue
		}
		var message domain.Message
		if err := json.Unmarshal(raw, &message); err != nil {
			return 0, fmt.Errorf("decode secretary pending message: %w", err)
		}
		visible := hash != "" && domain.SearchVisibleMessageMetadata(message) &&
			(expectedAgent == "" || expectedAgent == agent && expectedSession == session)
		if visible &&
			(hash != expectedHash || canonical != domain.SearchCanonicalizationVersion) ||
			!visible && expectedHash != "" {
			pending[source] = true
		}
	}
	return len(pending), rows.Err()
}

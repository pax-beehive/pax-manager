package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Materialize replacement classification once per relevant display, not per source.
const searchMessageBatchAccessSQL = `WITH targets AS MATERIALIZED (
 SELECT message_id,agent_id,session_id FROM messages
 WHERE message_id IN (SELECT jsonb_array_elements_text($1::jsonb))
), target_sessions AS MATERIALIZED (
 SELECT DISTINCT agent_id,session_id FROM targets
), relevant_displays AS MATERIALIZED (
 SELECT display.agent_id,display.session_id,display.parent_message_id,
 CASE WHEN jsonb_typeof(display.raw_json->'replaces_message_ids')='array' THEN
   CASE WHEN display.raw_json->'replaces_message_ids'<>'[]'::jsonb
     AND NOT EXISTS (
       SELECT 1 FROM jsonb_array_elements(display.raw_json->'replaces_message_ids') item
       WHERE jsonb_typeof(item) NOT IN ('string','null'))
   THEN display.raw_json->'replaces_message_ids' END
 END AS replacement_ids
 FROM messages display JOIN target_sessions session
 ON display.agent_id=session.agent_id
 AND display.session_id IS NOT DISTINCT FROM session.session_id
 WHERE display.message_type IN ('pax:invocation','pax:invocation_pending','pax:artifact')
), replacement_targets AS MATERIALIZED (
 SELECT display.agent_id,display.session_id,item.value #>> '{}' AS message_id
 FROM relevant_displays display CROSS JOIN LATERAL jsonb_array_elements(
 CASE WHEN display.replacement_ids IS NOT NULL THEN display.replacement_ids
 ELSE jsonb_build_array(display.parent_message_id) END) item(value)
 WHERE jsonb_typeof(item.value)='string'
)
SELECT ` + searchMessageProjectionSQL + `,a.owner_user_id,
 COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.part_index)
 FROM message_parts p WHERE p.message_id=m.message_id),'[]'::jsonb),0` +
	searchMessageVisibilityFromSQL + `
 JOIN targets target ON target.message_id=m.message_id
 WHERE TRUE` + searchMessageSourceVisibilitySQL + `
 AND NOT EXISTS (
 SELECT 1 FROM replacement_targets replacement
 WHERE replacement.agent_id=m.agent_id
 AND replacement.session_id IS NOT DISTINCT FROM m.session_id
 AND replacement.message_id=m.message_id
 )`

// Batch current canonical visibility checks without a round trip for every leaf.
func validateSearchMessageAccess(
	ctx context.Context, tx *sql.Tx, owner string, ids []string,
) error {
	_, err := validateSearchMessageAccessWithNode(ctx, tx, owner, ids)
	return err
}

func validateSearchMessageAccessWithNode(
	ctx context.Context, tx *sql.Tx, owner string, ids []string,
) (string, error) {
	if len(ids) == 0 {
		return "", nil
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
	rows, err := tx.QueryContext(ctx, searchMessageBatchAccessSQL, string(encoded))
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	visible := make(map[string]bool, len(expected))
	var nodeID string
	unknown := false
	for rows.Next() {
		doc, readErr := scanCanonicalSearchMessage(rows, 0)
		if readErr != nil {
			return "", readErr
		}
		if doc.OwnerUserID != owner || !expected[doc.SourceID] {
			return "", ErrNotFound
		}
		if doc.NodeID == "" || (nodeID != "" && nodeID != doc.NodeID) {
			unknown = true
		}
		nodeID = doc.NodeID
		visible[doc.SourceID] = true
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(visible) != len(expected) {
		return "", domain.ErrNotFound
	}
	if unknown {
		nodeID = ""
	}
	return nodeID, rows.Close()
}

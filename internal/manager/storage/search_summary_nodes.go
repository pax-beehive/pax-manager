package storage

import (
	"context"
	"database/sql"
)

// Primary-session metadata is a source for every pinned dependency, not only the root.
func summarySearchPrimaryNodesMatch(
	ctx context.Context, tx *sql.Tx, owner, summaryID string, revision int64, nodeID string,
) (bool, error) {
	if nodeID == "" {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE dependencies(summary_id,revision) AS (
 SELECT $1::text,$2::bigint
 UNION SELECT src.child_summary_id,src.child_revision FROM summary_sources src
 JOIN dependencies d USING (summary_id,revision) WHERE src.source_kind='summary'
 ) SELECT DISTINCT COALESCE(s.node_id,'') FROM dependencies d
 JOIN summary_versions v USING (summary_id,revision)
 JOIN agent_sessions s ON v.subject_type='fragment' AND v.subject_id='secretary:session:'||s.id::text
 JOIN agents a ON a.agent_id=s.agent_id
 LEFT JOIN nodes an ON an.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE v.owner_user_id=$3 AND a.owner_user_id=$3
 AND a.deleted_at IS NULL AND an.deleted_at IS NULL AND sn.deleted_at IS NULL
 AND s.archived_at IS NULL AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT starts_with(s.session_id,'sess_secretary_') LIMIT 4097`, summaryID, revision, owner)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sourceNodeID string
		if err = rows.Scan(&sourceNodeID); err != nil {
			return false, err
		}
		if sourceNodeID != nodeID {
			return false, nil
		}
	}
	return true, rows.Err()
}

package storage

import (
	"context"
	"database/sql"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func enrichSummaryNodeTx(ctx context.Context, tx *sql.Tx, result *domain.SummaryVersion) error {
	var status string
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT n.status,n.current_revision,COALESCE(origin.agent_id,''),
 COALESCE(origin.session_id,''),COALESCE(origin.title,'') FROM knowledge_nodes n
 LEFT JOIN LATERAL (SELECT s.agent_id,s.session_id,COALESCE(NULLIF(s.custom_session_name,''),s.session_name,'') title
 FROM agent_sessions s JOIN agents a ON a.agent_id=s.agent_id
 LEFT JOIN nodes an ON an.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE s.id::text=n.origin_source_id AND a.owner_user_id=n.owner_user_id
 AND a.deleted_at IS NULL AND an.deleted_at IS NULL AND sn.deleted_at IS NULL AND s.archived_at IS NULL
 AND COALESCE(s.source,'')<>'secretary_internal' AND NOT starts_with(s.session_id,'sess_secretary_')) origin ON TRUE
 WHERE n.id=$1 AND n.owner_user_id=$2`, result.SummaryID, result.OwnerUserID).
		Scan(&status, &current, &result.OriginAgentID, &result.OriginSessionID, &result.OriginSessionTitle)
	if err != nil {
		return err
	}
	if current == result.Revision {
		result.NodeStatus = status
	}
	if result.OriginAgentID != "" {
		result.OriginSourceID = summaryOriginSource(*result)
	}
	return nil
}

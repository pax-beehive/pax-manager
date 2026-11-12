package storage

// Limit metadata before applying eligibility. Locked rows must wait or roll back
// the complete page; skipping one would permanently advance the persisted cursor.
const secretaryRunPageSQL = `WITH page AS MATERIALIZED (
 SELECT source_id FROM secretary_jobs WHERE owner_user_id=$1 AND source_id>$2 AND source_id<=$3
 ORDER BY source_id LIMIT $4 FOR UPDATE
), queued AS (
 UPDATE secretary_jobs j SET available_at=NOW(),attempts=0,last_error='',execution_blocked=FALSE,
 retry_epoch=retry_epoch+1,retry_generation=retry_generation+1,last_validation_failure='',
 auto_retry_key='',auto_retry_count=0,updated_at=NOW()-INTERVAL '30 seconds'
 FROM page p WHERE j.source_id=p.source_id AND j.owner_user_id=$1
 AND j.desired_revision>j.processed_revision AND NOT j.paused
 AND (j.lease_token='' OR j.lease_until IS NULL OR j.lease_until<=NOW())
 AND EXISTS(SELECT 1 FROM agent_sessions s JOIN agents a ON a.agent_id=s.agent_id
 LEFT JOIN nodes an ON an.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE s.id=j.source_id AND s.agent_id=j.source_agent_id AND s.session_id=j.source_session_id
 AND a.owner_user_id=$1 AND a.deleted_at IS NULL AND an.deleted_at IS NULL AND sn.deleted_at IS NULL
 AND s.archived_at IS NULL AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT starts_with(s.session_id,'sess_secretary_')
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id))
 AND NOT EXISTS(SELECT 1 FROM secretary_batches b WHERE b.source_id=j.source_id AND b.owner_user_id=$1
 AND b.state IN ('pending','running','output_received','unknown'))
 RETURNING j.source_id
) SELECT COALESCE((SELECT max(source_id) FROM page),$2),(SELECT count(*) FROM page),(SELECT count(*) FROM queued)`

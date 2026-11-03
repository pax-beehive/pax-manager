package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryStableHeadSQL = `'summary_secretary_session_'||left(encode(sha256(
 convert_to(a.owner_user_id,'UTF8')||decode('00','hex')||convert_to(s.id::text,'UTF8')),'hex'),32)`

const secretaryBackfillSQL = `WITH sources AS MATERIALIZED (
 SELECT s.id source_id,a.owner_user_id,s.agent_id,s.session_id,j.processed_revision,
 ($2::boolean OR j.source_id IS NULL OR j.owner_user_id<>a.owner_user_id OR
  (j.processed_revision>0 AND j.desired_revision<=j.processed_revision AND NOT EXISTS(
   SELECT 1 FROM summary_versions v WHERE v.summary_id=` + secretaryStableHeadSQL + `
   AND v.owner_user_id=a.owner_user_id))) full_reconcile
 FROM agent_sessions s JOIN agents a ON a.agent_id=s.agent_id
 JOIN secretary_settings cfg ON cfg.owner_user_id=a.owner_user_id AND cfg.enabled
 LEFT JOIN secretary_jobs j ON j.source_id=s.id
 LEFT JOIN nodes n ON n.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE ($1='' OR a.owner_user_id=$1) AND a.deleted_at IS NULL AND n.deleted_at IS NULL AND sn.deleted_at IS NULL
 AND s.archived_at IS NULL AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
), members AS (
 SELECT s.source_id,s.owner_user_id,s.agent_id,s.session_id,m.message_id,m.session_seq,m.id message_order
 FROM sources s JOIN messages m ON m.agent_id=s.agent_id AND m.session_id=s.session_id
 LEFT JOIN secretary_message_state state ON state.source_id=s.source_id AND state.message_id=m.message_id
 WHERE s.full_reconcile OR state.message_id IS NULL OR
 (state.change_revision<=s.processed_revision AND state.processed_hash<>'' AND state.processed_canonicalization_version<>$3)
 UNION ALL
 SELECT s.source_id,s.owner_user_id,s.agent_id,s.session_id,state.message_id,NULL::bigint,0::bigint
 FROM sources s JOIN secretary_message_state state ON state.source_id=s.source_id
 WHERE s.full_reconcile AND state.processed_hash<>'' AND NOT EXISTS(
  SELECT 1 FROM messages m WHERE m.message_id=state.message_id AND m.agent_id=s.agent_id AND m.session_id=s.session_id)
), source_rows AS (
 SELECT *,row_number() OVER(PARTITION BY source_id ORDER BY session_seq,message_order,message_id) ordinal,
 count(*) OVER(PARTITION BY source_id) total FROM members
), heads AS (
 INSERT INTO secretary_jobs(source_id,owner_user_id,source_agent_id,source_session_id,desired_revision)
 SELECT DISTINCT source_id,owner_user_id,agent_id,session_id,total FROM source_rows
 ON CONFLICT(source_id) DO UPDATE SET desired_revision=secretary_jobs.desired_revision+EXCLUDED.desired_revision,
 lease_token=CASE WHEN secretary_jobs.owner_user_id<>EXCLUDED.owner_user_id THEN '' ELSE secretary_jobs.lease_token END,
 lease_until=CASE WHEN secretary_jobs.owner_user_id<>EXCLUDED.owner_user_id THEN NULL ELSE secretary_jobs.lease_until END,
 owner_user_id=EXCLUDED.owner_user_id,updated_at=NOW(),available_at=NOW()
 RETURNING source_id,desired_revision,processed_revision
)
 INSERT INTO secretary_message_state(source_id,message_id,change_revision)
 SELECT r.source_id,r.message_id,h.desired_revision-r.total+r.ordinal FROM source_rows r JOIN heads h USING(source_id)
 ON CONFLICT(source_id,message_id) DO UPDATE SET change_revision=EXCLUDED.change_revision
 WHERE $2 OR secretary_message_state.change_revision<=(
  SELECT processed_revision FROM heads WHERE source_id=EXCLUDED.source_id)`

func reconcileSecretaryTx(ctx context.Context, tx *sql.Tx, owner string, full bool) error {
	// A separate statement gives waiting bootstraps a fresh READ COMMITTED snapshot.
	if !full {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
 current_database()||':'||current_schema()||':pax:secretary:bootstrap',0))`); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(
		ctx,
		secretaryBackfillSQL,
		owner,
		full,
		domain.SearchCanonicalizationVersion,
	)
	return err
}

func backfillSecretaryTx(ctx context.Context, tx *sql.Tx, owner string) error {
	return reconcileSecretaryTx(ctx, tx, owner, true)
}

func (s *PostgresStore) BackfillSecretaryJobs(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = reconcileSecretaryTx(ctx, tx, "", false); err != nil {
		return err
	}
	return tx.Commit()
}

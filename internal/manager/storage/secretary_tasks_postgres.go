package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryTaskStore = (*PostgresStore)(nil)

const secretaryTaskSelectSQL = `SELECT j.source_id::text,j.source_session_id,j.source_agent_id,
 COALESCE(NULLIF(s.custom_session_name,''),NULLIF(s.session_name,''),s.session_id),cfg.agent_id,
 j.control_revision,cfg.revision,j.retry_epoch,j.paused,
 COALESCE(j.lease_token<>'' AND j.lease_until>clock_timestamp(),FALSE),j.desired_revision,j.processed_revision,
 j.available_at,GREATEST(j.updated_at,b.updated_at,a.updated_at),s.created_at,s.runtime_status,cfg.enabled,
 COALESCE(b.batch_id,''),COALESCE(b.state,''),COALESCE(b.attempt_count,0),COALESCE(b.connection_failures,0),b.retry_after,
 COALESCE(a.attempt_id,''),COALESCE(a.state,''),COALESCE(a.not_dispatched,FALSE),
 CASE WHEN j.execution_blocked AND starts_with(j.last_error,'` + secretaryInputLimitPrefix + `')
 AND COALESCE(b.state,'') NOT IN ('running','unknown','output_received') THEN j.last_error
 ELSE COALESCE(a.last_error,j.last_error) END,j.execution_blocked
 FROM secretary_jobs j JOIN secretary_settings cfg ON cfg.owner_user_id=j.owner_user_id
 JOIN agents source ON source.agent_id=j.source_agent_id AND source.owner_user_id=j.owner_user_id AND source.deleted_at IS NULL
 JOIN agent_sessions s ON s.id=j.source_id AND s.agent_id=j.source_agent_id AND s.session_id=j.source_session_id AND s.archived_at IS NULL
 LEFT JOIN nodes source_node ON source_node.node_id=source.node_id LEFT JOIN nodes session_node ON session_node.node_id=s.node_id
 LEFT JOIN LATERAL(SELECT * FROM secretary_batches candidate WHERE candidate.source_id=j.source_id
 AND candidate.owner_user_id=j.owner_user_id AND candidate.settings_revision=cfg.revision AND candidate.retry_epoch=j.retry_epoch
 AND candidate.state<>'superseded' ORDER BY CASE WHEN candidate.state IN('pending','running','output_received','unknown') THEN 2
 WHEN candidate.state='blocked' THEN 1 ELSE 0 END DESC,candidate.updated_at DESC,candidate.batch_id DESC LIMIT 1) b ON TRUE
 LEFT JOIN secretary_attempts a ON a.attempt_id=b.current_attempt_id AND a.batch_id=b.batch_id
 WHERE j.owner_user_id=$1 AND source_node.deleted_at IS NULL AND session_node.deleted_at IS NULL
 AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)`

func scanSecretaryTask(row interface{ Scan(...any) error }) (secretaryTaskRecord, error) {
	var record secretaryTaskRecord
	var updated, created time.Time
	var retry sql.NullTime
	task := &record.task
	err := row.Scan(
		&task.TaskID,
		&task.SessionID,
		&task.SourceAgentID,
		&task.Title,
		&task.SecretaryAgentID,
		&task.Revision,
		&task.SettingsRevision,
		&task.RetryEpoch,
		&task.Paused,
		&task.LeaseActive,
		&record.desired,
		&record.processed,
		&record.available,
		&updated,
		&created,
		&record.sourceRuntime,
		&record.enabled,
		&task.BatchID,
		&record.batchState,
		&task.AttemptCount,
		&task.ConnectionFailures,
		&retry,
		&task.AttemptID,
		&record.attemptState,
		&record.notDispatched,
		&record.failure,
		&record.blocked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	task.CreatedAt, task.UpdatedAt, record.retryAfter = &created, &updated, retry.Time
	return record, err
}

func loadSecretaryTaskTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, id string,
) (secretaryTaskRecord, error) {
	record, err := scanSecretaryTask(
		tx.QueryRowContext(ctx, secretaryTaskSelectSQL+` AND j.source_id=$2`, owner, id),
	)
	if err == nil {
		err = enrichSecretaryTaskLinkTx(ctx, tx, owner, &record.task)
	}
	return record, err
}

func secretaryExecutionLinkTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, agent, attempt string,
) (string, string, string, error) {
	var sessionID, agentID, status string
	err := tx.QueryRowContext(ctx, `SELECT s.session_id,s.agent_id,s.runtime_status FROM agent_sessions s
 JOIN agents a ON a.agent_id=s.agent_id AND a.owner_user_id=$1 AND a.deleted_at IS NULL
 LEFT JOIN nodes n ON n.node_id=a.node_id LEFT JOIN nodes sn ON sn.node_id=s.node_id
 WHERE s.session_id=$2 AND ($3='' OR s.agent_id=$3) AND s.archived_at IS NULL
 AND n.deleted_at IS NULL AND sn.deleted_at IS NULL ORDER BY s.agent_id LIMIT 1`,
		owner, secretaryExecutionSessionID(owner, attempt), agent).
		Scan(&sessionID, &agentID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", nil
	}
	return sessionID, agentID, status, err
}

func enrichSecretaryTaskLinkTx(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	task *domain.SecretaryTask,
) error {
	if task.AttemptID == "" {
		return nil
	}
	var err error
	task.ExecutionSessionID, task.ExecutionAgentID, task.ExecutionStatus, err =
		secretaryExecutionLinkTx(ctx, tx, owner, task.SecretaryAgentID, task.AttemptID)
	return err
}

func (s *PostgresStore) ListSecretaryTasks(
	ctx context.Context,
	owner string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskPage, error) {
	after, limit, err := secretaryTaskAfter(owner, query)
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, secretaryTaskSelectSQL+`
 AND j.desired_revision>j.processed_revision AND j.source_id>$2 ORDER BY j.source_id LIMIT $3`, owner, after, limit+1)
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	var records []secretaryTaskRecord
	for rows.Next() {
		record, scanErr := scanSecretaryTask(rows)
		if scanErr != nil {
			_ = rows.Close()
			return domain.SecretaryTaskPage{}, scanErr
		}
		records = append(records, record)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	page := domain.SecretaryTaskPage{Items: []domain.SecretaryTask{}}
	if len(records) > limit {
		page.NextCursor = secretaryTaskNextCursor(owner, "list", records[limit-1].task.TaskID)
		records = records[:limit]
	}
	for _, record := range records {
		if err = enrichSecretaryTaskLinkTx(ctx, tx, owner, &record.task); err != nil {
			return domain.SecretaryTaskPage{}, err
		}
		page.Items = append(page.Items, secretaryTaskView(record, s.now()))
	}
	return page, tx.Commit()
}

func (s *PostgresStore) GetSecretaryTask(
	ctx context.Context,
	owner, id string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskDetail, error) {
	after, afterID, limit, err := secretaryTaskAttemptAfter(owner, id, query)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	if _, err = secretaryTaskID(id); err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := loadSecretaryTaskTx(ctx, tx, owner, id)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	detail := domain.SecretaryTaskDetail{
		Task:     secretaryTaskView(record, s.now()),
		Attempts: []domain.SecretaryTaskAttempt{},
	}
	var boundary any
	if !after.IsZero() {
		boundary = after
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT a.attempt_id,a.batch_id,a.state,a.not_dispatched,a.created_at,a.updated_at,
 octet_length(a.output_text)>0,a.last_error,b.state FROM secretary_batches b JOIN secretary_attempts a USING(batch_id)
 WHERE b.owner_user_id=$1 AND b.source_id=$2 AND ($3::timestamptz IS NULL OR (a.created_at,a.attempt_id)<($3,$4))
 ORDER BY a.created_at DESC,a.attempt_id DESC LIMIT $5`,
		owner,
		id,
		boundary,
		afterID,
		limit+1,
	)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	for rows.Next() {
		var item domain.SecretaryTaskAttempt
		var failure, batchState string
		if err = rows.Scan(&item.AttemptID, &item.BatchID, &item.State, &item.NotDispatched,
			&item.CreatedAt, &item.UpdatedAt, &item.HasOutput, &failure, &batchState); err != nil {
			_ = rows.Close()
			return domain.SecretaryTaskDetail{}, err
		}
		item.ErrorCode, item.ErrorMessage = secretaryTaskFailure(
			batchState,
			item.State,
			failure,
			item.NotDispatched,
		)
		detail.Attempts = append(detail.Attempts, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	if len(detail.Attempts) > limit {
		detail.NextCursor = secretaryTaskAttemptCursor(owner, id, detail.Attempts[limit-1])
		detail.Attempts = detail.Attempts[:limit]
	}
	for index := range detail.Attempts {
		item := &detail.Attempts[index]
		item.ExecutionSessionID, item.ExecutionAgentID, item.ExecutionStatus, err =
			secretaryExecutionLinkTx(ctx, tx, owner, "", item.AttemptID)
		if err != nil {
			return domain.SecretaryTaskDetail{}, err
		}
	}
	return detail, tx.Commit()
}

func lockSecretaryTaskTx(
	ctx context.Context,
	s *PostgresStore,
	owner, id string,
) (*sql.Tx, secretaryTaskRecord, error) {
	if owner == "" {
		return nil, secretaryTaskRecord{}, ErrUnauthorized
	}
	if _, err := secretaryTaskID(id); err != nil {
		return nil, secretaryTaskRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, secretaryTaskRecord{}, err
	}
	var locked string
	err = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM secretary_settings WHERE owner_user_id=$1 FOR SHARE`, owner).
		Scan(&locked)
	if err == nil {
		err = tx.QueryRowContext(ctx, `SELECT source_id::text FROM secretary_jobs WHERE source_id=$1 AND owner_user_id=$2 FOR UPDATE`, id, owner).
			Scan(&locked)
	}
	var record secretaryTaskRecord
	if err == nil {
		record, err = loadSecretaryTaskTx(ctx, tx, owner, id)
	}
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return nil, record, err
	}
	return tx, record, nil
}

func (s *PostgresStore) ControlSecretaryTask(
	ctx context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
) (domain.SecretaryTask, error) {
	tx, record, err := lockSecretaryTaskTx(ctx, s, owner, id)
	if err != nil {
		return domain.SecretaryTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateSecretaryTaskAction(secretaryTaskView(record, s.now()), action); err != nil {
		return domain.SecretaryTask{}, err
	}
	switch action.Action {
	case "pause", "resume":
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET paused=$2,control_revision=control_revision+1 WHERE source_id=$1`,
			id,
			action.Action == "pause",
		)
	case "retry":
		if !record.enabled {
			return domain.SecretaryTask{}, ErrConflict
		}
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET retry_epoch=retry_epoch+1,retry_generation=retry_generation+1,
 paused=FALSE,execution_blocked=FALSE,control_revision=control_revision+1,lease_token='',lease_until=NULL,
 available_at=NOW(),updated_at=NOW()-INTERVAL '30 seconds',attempts=0,last_error='',last_validation_failure='',
 auto_retry_key='',auto_retry_count=0 WHERE source_id=$1`,
			id,
		)
	default:
		return domain.SecretaryTask{}, ErrConflict
	}
	if err != nil {
		return domain.SecretaryTask{}, err
	}
	record, err = loadSecretaryTaskTx(ctx, tx, owner, id)
	if err != nil {
		return domain.SecretaryTask{}, err
	}
	return secretaryTaskView(record, s.now()), tx.Commit()
}

func (s *PostgresStore) ClaimSecretaryTaskRecovery(
	ctx context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
	lease time.Duration,
) (domain.SecretaryJob, error) {
	tx, record, err := lockSecretaryTaskTx(ctx, s, owner, id)
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = validateSecretaryRecoveryAction(secretaryTaskView(record, s.now()), action, record.enabled); err != nil {
		return domain.SecretaryJob{}, err
	}
	current := domain.SecretaryJob{
		SourceID:         id,
		OwnerUserID:      owner,
		SourceAgentID:    record.task.SourceAgentID,
		SourceSessionID:  record.task.SessionID,
		AgentID:          record.task.SecretaryAgentID,
		SettingsRevision: record.task.SettingsRevision,
		RetryEpoch:       record.task.RetryEpoch,
		BatchID:          record.task.BatchID,
		ThroughRevision:  record.desired,
	}
	if err = tx.QueryRowContext(ctx, `SELECT retry_generation FROM secretary_jobs WHERE source_id=$1`, id).Scan(&current.RetryGeneration); err != nil {
		return domain.SecretaryJob{}, err
	}
	batch, err := loadSecretaryExecutionBatch(ctx, tx, current)
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	job, err := secretaryRestoreBatch(batch.input, current)
	if err == nil {
		err = validateSecretaryBatchIdentity(job, current)
	}
	if err == nil {
		err = validateSecretaryRecoveryInputTx(ctx, tx, job)
	}
	if err != nil {
		return domain.SecretaryJob{}, ErrConflict
	}
	attempt, err := loadSecretaryAttempt(ctx, tx, record.task.AttemptID)
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	job.Attempt, job.AttemptID, job.RunID = &attempt, attempt.ID, attempt.ID
	job.LeaseToken, err = newSecret("secretary_recheck")
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET lease_token=$2,lease_until=NOW()+$3*INTERVAL '1 millisecond',
 execution_blocked=TRUE,control_revision=control_revision+1 WHERE source_id=$1`,
		id,
		job.LeaseToken,
		secretaryRecoveryLease(lease).Milliseconds(),
	)
	if err == nil {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_batches SET lease_token=$2 WHERE batch_id=$1`,
			job.BatchID,
			job.LeaseToken,
		)
	}
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	var active bool
	err = tx.QueryRowContext(ctx, secretaryActiveSQL, job.SourceID, job.OwnerUserID,
		job.AgentID, job.SettingsRevision, job.LeaseToken).Scan(&active)
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	if !active {
		return domain.SecretaryJob{}, ErrConflict
	}
	return job, tx.Commit()
}

func validateSecretaryRecoveryInputTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) error {
	if err := validateSecretaryBatchInputTx(ctx, tx, job); err != nil {
		return err
	}
	if err := validateSecretaryNodeInputsTx(ctx, tx, job); err != nil {
		return err
	}
	for _, summary := range job.CurrentSummaries {
		current, err := getSummaryTx(ctx, tx, job.OwnerUserID, summary.SummaryID, 0)
		if err != nil || current.Revision != summary.Revision {
			return ErrConflict
		}
	}
	return nil
}

func (s *PostgresStore) ReleaseSecretaryTaskRecovery(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lease string
	err = tx.QueryRowContext(ctx, `SELECT lease_token FROM secretary_jobs WHERE source_id=$1 AND owner_user_id=$2 FOR UPDATE`,
		job.SourceID, job.OwnerUserID).
		Scan(&lease)
	if errors.Is(err, sql.ErrNoRows) || job.LeaseToken == "" {
		return domain.ErrSecretaryFenced
	}
	if err != nil {
		return err
	}
	batch, err := loadSecretaryExecutionBatch(ctx, tx, job)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrSecretaryFenced
	}
	if err != nil {
		return err
	}
	if lease != job.LeaseToken || batch.lease != job.LeaseToken {
		if batch.state == "committed" {
			return tx.Commit()
		}
		return domain.ErrSecretaryFenced
	}
	proven := false
	if batch.state == "unknown" {
		proven, err = secretaryExecutionTerminalTx(ctx, tx, job)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET lease_token='',lease_until=NULL,
 execution_blocked=CASE WHEN $2='committed' THEN FALSE WHEN $2='pending' THEN execution_blocked ELSE NOT $3 END WHERE source_id=$1`, job.SourceID, batch.state, proven)
	if err != nil {
		return err
	}
	return tx.Commit()
}

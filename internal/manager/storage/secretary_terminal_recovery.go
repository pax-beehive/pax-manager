package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryKnownFailureRetryDelay(attempts int) time.Duration {
	if attempts > 1 {
		return 10 * time.Second
	}
	return 5 * time.Second
}

func secretaryKnownExecutionFailure(failure error) bool {
	return errors.Is(failure, domain.ErrSecretaryExecutionFailed) &&
		!errors.Is(failure, domain.ErrSecretaryExecutionUnknown)
}

func (s *MemoryStore) secretaryExecutionTerminalLocked(job domain.SecretaryJob) bool {
	request, ok := s.messages[domain.SecretaryExecutionRequestMessageID(job)]
	if !ok || !s.secretaryLocked().internal[sessionKey(job.AgentID, request.SessionID)] {
		return false
	}
	terminal, ok := s.messages[domain.SecretaryExecutionTerminalMessageID(request)]
	return ok && domain.SecretaryExecutionTerminalMatches(request, terminal, job)
}

func secretaryExecutionTerminalTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (bool, error) {
	request, err := scanMessage(
		tx.QueryRowContext(
			ctx,
			`SELECT `+messageReturningSQL+` FROM messages WHERE message_id=$1`,
			domain.SecretaryExecutionRequestMessageID(job),
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var internal bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secretary_internal_sessions WHERE owner_user_id=$1 AND agent_id=$2 AND session_id=$3)`, job.OwnerUserID, job.AgentID, request.SessionID).Scan(&internal); err != nil ||
		!internal {
		return false, err
	}
	terminal, err := scanMessage(
		tx.QueryRowContext(
			ctx,
			`SELECT `+messageReturningSQL+` FROM messages WHERE message_id=$1`,
			domain.SecretaryExecutionTerminalMessageID(request),
		),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return domain.SecretaryExecutionTerminalMatches(request, terminal, job), nil
}

func secretaryTerminalWakeMatches(
	record secretaryTaskRecord,
	job domain.SecretaryJob,
	generation int64,
) bool {
	return record.enabled && record.task.SecretaryAgentID == job.AgentID &&
		record.task.SettingsRevision == job.SettingsRevision &&
		record.task.RetryEpoch == job.RetryEpoch &&
		generation == job.RetryGeneration &&
		record.task.BatchID == job.BatchID &&
		record.task.AttemptID == job.AttemptID &&
		!record.notDispatched &&
		secretaryTerminalRecoverable(record.batchState) &&
		secretaryTerminalRecoverable(record.attemptState)
}

func secretaryTerminalRecoverable(
	state string,
) bool {
	return state == "running" || state == "unknown"
}

func secretaryTerminalWakeInputMatches(
	input []byte,
	job domain.SecretaryJob,
	record secretaryTaskRecord,
) bool {
	current := job
	current.SourceAgentID, current.SourceSessionID, current.ThroughRevision = record.task.SourceAgentID, record.task.SessionID, record.desired
	var frozen domain.SecretaryJob
	return json.Unmarshal(input, &frozen) == nil &&
		validateSecretaryBatchIdentity(frozen, current) == nil
}

// WakeSecretaryExecutionRecovery only schedules recovery of the original attempt.
func (s *MemoryStore) WakeSecretaryExecutionRecovery(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := s.secretaryLocked().jobs[job.SourceID]
	record, err := s.memorySecretaryTaskLocked(job.OwnerUserID, job.SourceID)
	if err != nil || !s.secretaryAgentOwnedLocked(job.OwnerUserID, job.AgentID) || queued == nil ||
		!secretaryTerminalWakeMatches(record, job, queued.work.RetryGeneration) {
		return nil
	}
	batch := s.secretaryExecutionsLocked()[job.BatchID]
	if batch == nil || !secretaryTerminalWakeInputMatches(batch.input, job, record) ||
		!s.secretaryExecutionTerminalLocked(job) {
		return nil
	}
	queued.executionBlocked = false
	return nil
}

func (s *PostgresStore) WakeSecretaryExecutionRecovery(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	record, current, err := lockSecretaryTerminalWakeTx(ctx, tx, job)
	if err != nil || !current {
		return err
	}
	batch, err := loadSecretaryExecutionBatch(ctx, tx, job)
	if err != nil {
		return err
	}
	if !secretaryTerminalWakeInputMatches(batch.input, job, record) {
		return nil
	}
	proven, err := secretaryExecutionTerminalTx(ctx, tx, job)
	if err != nil || !proven {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET execution_blocked=FALSE WHERE source_id=$1`,
		job.SourceID,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func lockSecretaryTerminalWakeTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (secretaryTaskRecord, bool, error) {
	// Use the same settings/job lock order as execution failure transitions.
	var enabled bool
	var agent string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT cfg.enabled,cfg.agent_id,cfg.revision FROM secretary_settings cfg
 JOIN agents selected ON selected.agent_id=cfg.agent_id AND selected.owner_user_id=cfg.owner_user_id AND selected.deleted_at IS NULL
 LEFT JOIN nodes selected_node ON selected_node.node_id=selected.node_id
 WHERE cfg.owner_user_id=$1 AND selected_node.deleted_at IS NULL FOR UPDATE OF cfg`, job.OwnerUserID).
		Scan(&enabled, &agent, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return secretaryTaskRecord{}, false, nil
	}
	if err != nil {
		return secretaryTaskRecord{}, false, err
	}
	if !enabled || agent != job.AgentID || revision != job.SettingsRevision {
		return secretaryTaskRecord{}, false, nil
	}
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT retry_generation FROM secretary_jobs WHERE source_id=$1 AND owner_user_id=$2 FOR UPDATE`, job.SourceID, job.OwnerUserID).
		Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return secretaryTaskRecord{}, false, nil
	}
	if err != nil {
		return secretaryTaskRecord{}, false, err
	}
	record, err := loadSecretaryTaskTx(ctx, tx, job.OwnerUserID, job.SourceID)
	if errors.Is(err, ErrNotFound) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	return record, secretaryTerminalWakeMatches(record, job, generation), nil
}

func failSecretaryAttemptJobTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	state string,
	failure error,
	attempts int,
) error {
	switch {
	case state == "superseded":
		_, err := tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET retry_epoch=retry_epoch+1,execution_blocked=FALSE WHERE source_id=$1`,
			job.SourceID,
		)
		return err
	case state == "unknown":
		proven, err := secretaryExecutionTerminalTx(ctx, tx, job)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET execution_blocked=$2 WHERE source_id=$1`,
			job.SourceID,
			!proven,
		)
		return err
	case state == "pending" && secretaryKnownExecutionFailure(failure):
		_, err := tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET execution_blocked=FALSE,available_at=NOW()+$2*INTERVAL '1 millisecond' WHERE source_id=$1`,
			job.SourceID,
			secretaryKnownFailureRetryDelay(attempts).Milliseconds(),
		)
		return err
	case state != "pending":
		_, err := tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET execution_blocked=(desired_revision<=(SELECT through_revision FROM secretary_batches WHERE batch_id=$2)) WHERE source_id=$1`,
			job.SourceID,
			job.BatchID,
		)
		return err
	default:
		return nil
	}
}

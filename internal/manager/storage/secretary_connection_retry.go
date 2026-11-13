package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryNotDispatched(failure error) bool {
	return errors.Is(failure, domain.ErrSecretaryNotDispatched) &&
		!errors.Is(failure, domain.ErrSecretaryExecutionUnknown)
}

func secretaryConnectionExhausted(state string, failures int) bool {
	return state == "blocked" && failures > domain.SecretaryConnectionMaxRetries
}

func secretaryConnectionDeferred(state string, retryAfter, now time.Time) bool {
	return state == "pending" && retryAfter.After(now)
}

func (s *MemoryStore) failSecretaryConnectionLocked(
	job domain.SecretaryJob,
	batch *memorySecretaryExecutionBatch,
	attempt *domain.SecretaryAttempt,
	failure error,
) {
	batch.connectionFailures++
	batch.state = "pending"
	batch.retryAfter = s.now().
		Add(domain.SecretaryConnectionRetryDelay(batch.connectionFailures, attempt.ID))
	if batch.connectionFailures > domain.SecretaryConnectionMaxRetries {
		batch.state = "blocked"
		batch.retryAfter = time.Time{}
	}
	attempt.State, attempt.NotDispatched = "failed", true
	attempt.Error, attempt.UpdatedAt = secretaryErrorText(failure), s.now().UTC()
	queued := s.secretaryLocked().jobs[job.SourceID]
	queued.executionBlocked = batch.state == "blocked"
	queued.available = batch.retryAfter
	if queued.executionBlocked {
		queued.available = s.now()
	}
	queued.work.LeaseToken, queued.leaseUntil = "", time.Time{}
	queued.lastError = attempt.Error
	s.failMemoryOperationWorkLocked(job, batch.state, failure)
}

func (s *MemoryStore) gateSecretaryConnectionLocked(
	job domain.SecretaryJob,
	batch *memorySecretaryExecutionBatch,
) error {
	exhausted := secretaryConnectionExhausted(batch.state, batch.connectionFailures)
	if !exhausted && !secretaryConnectionDeferred(batch.state, batch.retryAfter, s.now()) {
		return nil
	}
	queued := s.secretaryLocked().jobs[job.SourceID]
	queued.executionBlocked = exhausted
	queued.work.LeaseToken, queued.leaseUntil = "", time.Time{}
	if exhausted {
		return domain.ErrSecretaryExecutionBlocked
	}
	queued.available = batch.retryAfter
	return domain.ErrSecretaryRetryDeferred
}

func (s *MemoryStore) secretaryConnectionClaimableLocked(
	job *memorySecretaryJob,
	settings int64,
) bool {
	for _, batch := range s.secretaryExecutionsLocked() {
		if batch.source != job.work.SourceID || batch.owner != job.work.OwnerUserID ||
			batch.settings != settings || batch.epoch != job.work.RetryEpoch {
			continue
		}
		if secretaryConnectionExhausted(batch.state, batch.connectionFailures) ||
			secretaryConnectionDeferred(batch.state, batch.retryAfter, s.now()) {
			return false
		}
	}
	return true
}

func failSecretaryConnectionTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	batch secretaryExecutionBatch,
	attempt domain.SecretaryAttempt,
	failure error,
	now time.Time,
) error {
	if err := writeSecretaryConnectionFailureTx(ctx, tx, job, batch, attempt, failure, now); err != nil {
		return err
	}
	return tx.Commit()
}

func writeSecretaryConnectionFailureTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	batch secretaryExecutionBatch,
	attempt domain.SecretaryAttempt,
	failure error,
	now time.Time,
) error {
	failures := batch.connectionFailures + 1
	state := "pending"
	var deadline any = now.Add(domain.SecretaryConnectionRetryDelay(failures, attempt.ID))
	blocked := failures > domain.SecretaryConnectionMaxRetries
	if blocked {
		state, deadline = "blocked", nil
	}
	errorText := secretaryErrorText(failure)
	_, err := tx.ExecContext(ctx, `UPDATE secretary_attempts SET state='failed',not_dispatched=TRUE,
 last_error=$2,updated_at=NOW() WHERE attempt_id=$1`, attempt.ID, errorText)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE secretary_batches SET state=$2,connection_failures=$3,
 retry_after=$4,updated_at=NOW() WHERE batch_id=$1`, job.BatchID, state, failures, deadline)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET execution_blocked=$2,
 available_at=COALESCE($3::timestamptz,NOW()),last_error=$4,attempts=attempts+1,
 lease_token='',lease_until=NULL WHERE source_id=$1`, job.SourceID, blocked, deadline, errorText)
	}
	if err != nil {
		return err
	}
	if blocked {
		return failSecretaryOperationWorkTx(ctx, tx, job, state, failure)
	}
	return nil
}

func gateSecretaryConnectionTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	batch secretaryExecutionBatch,
	now time.Time,
) error {
	exhausted := secretaryConnectionExhausted(batch.state, batch.connectionFailures)
	if !exhausted && !secretaryConnectionDeferred(batch.state, batch.retryAfter.Time, now) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE secretary_jobs SET execution_blocked=$2,
 available_at=COALESCE($3::timestamptz,NOW()),lease_token='',lease_until=NULL WHERE source_id=$1`,
		job.SourceID, exhausted, batch.retryAfter)
	if err != nil {
		return err
	}
	if exhausted {
		return domain.ErrSecretaryExecutionBlocked
	}
	return domain.ErrSecretaryRetryDeferred
}

func commitSecretaryConnectionGate(tx *sql.Tx, failure error) error {
	if !errors.Is(failure, domain.ErrSecretaryRetryDeferred) &&
		!errors.Is(failure, domain.ErrSecretaryExecutionBlocked) {
		return failure
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return failure
}

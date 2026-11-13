package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type secretaryExecutionBatch struct {
	input                []byte
	state                string
	lease                string
	attemptID            string
	attemptCount         int
	connectionFailures   int
	undispatchedAttempts int
	retryAfter           sql.NullTime
}

func secretaryExecutionTx(
	ctx context.Context,
	s *PostgresStore,
	job domain.SecretaryJob,
) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = lockSecretaryJob(ctx, tx, job); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func loadSecretaryExecutionBatch(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (secretaryExecutionBatch, error) {
	var batch secretaryExecutionBatch
	err := tx.QueryRowContext(ctx, `SELECT input_json,state,lease_token,current_attempt_id,attempt_count,connection_failures,retry_after,
 (SELECT count(*) FROM secretary_attempts a WHERE a.batch_id=secretary_batches.batch_id AND a.not_dispatched) FROM secretary_batches
 WHERE batch_id=$1 AND owner_user_id=$2 AND source_id=$3 FOR UPDATE`, job.BatchID, job.OwnerUserID, job.SourceID).
		Scan(&batch.input, &batch.state, &batch.lease, &batch.attemptID, &batch.attemptCount, &batch.connectionFailures, &batch.retryAfter, &batch.undispatchedAttempts)
	return batch, err
}

func loadSecretaryAttempt(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (domain.SecretaryAttempt, error) {
	var attempt domain.SecretaryAttempt
	var reads []byte
	err := tx.QueryRowContext(ctx, `SELECT attempt_id,batch_id,state,output_text,reads_json,last_error,created_at,updated_at,not_dispatched
 FROM secretary_attempts WHERE attempt_id=$1`, id).
		Scan(&attempt.ID, &attempt.BatchID, &attempt.State, &attempt.Output, &reads, &attempt.Error, &attempt.CreatedAt, &attempt.UpdatedAt, &attempt.NotDispatched)
	if err == nil {
		err = json.Unmarshal(reads, &attempt.Reads)
	}
	return attempt, err
}

func pendingSecretaryBatchIDTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (string, error) {
	var pendingID string
	err := tx.QueryRowContext(ctx, `SELECT batch_id FROM secretary_batches WHERE source_id=$1 AND owner_user_id=$2
 AND settings_revision=$3 AND retry_epoch=$4 AND (state IN ('pending','running','output_received','unknown')
 OR (state='blocked' AND connection_failures>8))
 ORDER BY created_at LIMIT 1 FOR UPDATE`, job.SourceID, job.OwnerUserID, job.SettingsRevision, job.RetryEpoch).
		Scan(&pendingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if pendingID != "" {
		return pendingID, nil
	}
	return job.BatchID, nil
}

func (s *PostgresStore) PrepareSecretaryBatch(
	ctx context.Context,
	job domain.SecretaryJob,
) (domain.SecretaryJob, error) {
	tx, err := secretaryExecutionTx(ctx, s, job)
	if err != nil {
		return job, err
	}
	defer func() { _ = tx.Rollback() }()
	job.BatchID = secretaryBatchID(job)
	job.BatchID, err = pendingSecretaryBatchIDTx(ctx, tx, job)
	if err != nil {
		return job, err
	}
	batch, err := loadSecretaryExecutionBatch(ctx, tx, job)
	if errors.Is(err, sql.ErrNoRows) {
		complete, completeErr := secretarySourcesCompleteTx(ctx, tx, job)
		if completeErr != nil {
			return job, completeErr
		}
		if !complete {
			if _, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET lease_token='',lease_until=NULL,
 available_at=NOW()+INTERVAL '30 seconds',last_error=$2 WHERE source_id=$1`, job.SourceID, domain.ErrSecretarySourceIncomplete.Error()); err != nil {
				return job, err
			}
			if err = tx.Commit(); err != nil {
				return job, err
			}
			return job, domain.ErrSecretarySourceIncomplete
		}
		batch.input, batch.state = secretaryBatchInput(job), "pending"
		batch.connectionFailures, batch.retryAfter, err = inheritSecretaryConnectionTx(ctx, tx, job)
		if err != nil {
			return job, err
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO secretary_batches(batch_id,source_id,owner_user_id,input_json,state,lease_token,settings_revision,retry_epoch,through_revision,connection_failures,retry_after)
 VALUES($1,$2,$3,$4,'pending',$5,$6,$7,$8,$9,$10)`,
			job.BatchID,
			job.SourceID,
			job.OwnerUserID,
			string(batch.input),
			job.LeaseToken,
			job.SettingsRevision,
			job.RetryEpoch,
			job.ThroughRevision,
			batch.connectionFailures,
			batch.retryAfter,
		)
	}
	if err != nil {
		return job, err
	}
	if err = gateSecretaryConnectionTx(ctx, tx, job, batch, s.now()); err != nil {
		return job, commitSecretaryConnectionGate(tx, err)
	}
	if batch.state == "blocked" || batch.state == "superseded" {
		if _, err = tx.ExecContext(ctx, `UPDATE secretary_jobs SET execution_blocked=TRUE WHERE source_id=$1`, job.SourceID); err != nil {
			return job, err
		}
		if err = tx.Commit(); err != nil {
			return job, err
		}
		return job, domain.ErrSecretaryExecutionBlocked
	}
	restored, err := secretaryRestoreBatch(batch.input, job)
	if err == nil {
		err = validateSecretaryBatchIdentity(restored, job)
	}
	if err == nil {
		err = validateSecretaryBatchInputTx(ctx, tx, restored)
	}
	if err != nil {
		return domain.SecretaryJob{}, rejectSecretaryBatchInputTx(ctx, tx, job, batch, err)
	}
	if batch.attemptID != "" {
		attempt, readErr := loadSecretaryAttempt(ctx, tx, batch.attemptID)
		if readErr != nil {
			return job, readErr
		}
		restored.Attempt, restored.AttemptID, restored.RunID = &attempt, attempt.ID, attempt.ID
	}
	if _, err = tx.ExecContext(ctx, `UPDATE secretary_batches SET lease_token=$2,updated_at=NOW() WHERE batch_id=$1`, job.BatchID, job.LeaseToken); err != nil {
		return job, err
	}
	return restored, commitPreparedSecretaryBatchTx(ctx, tx, job.SourceID)
}

func (s *PostgresStore) BeginSecretaryAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
) (domain.SecretaryAttempt, error) {
	tx, err := secretaryExecutionTx(ctx, s, job)
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := loadSecretaryExecutionBatch(ctx, tx, job)
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	if batch.lease != job.LeaseToken {
		return domain.SecretaryAttempt{}, domain.ErrSecretaryFenced
	}
	if err = gateSecretaryConnectionTx(ctx, tx, job, batch, s.now()); err != nil {
		return domain.SecretaryAttempt{}, commitSecretaryConnectionGate(tx, err)
	}
	if batch.state != "pending" {
		if batch.state == "running" {
			_, err = tx.ExecContext(
				ctx,
				`UPDATE secretary_batches SET state='unknown',updated_at=NOW() WHERE batch_id=$1`,
				job.BatchID,
			)
			if err == nil {
				_, err = tx.ExecContext(
					ctx,
					`UPDATE secretary_attempts SET state='unknown',updated_at=NOW() WHERE attempt_id=$1 AND state='running'`,
					batch.attemptID,
				)
			}
		}
		if err == nil {
			_, err = tx.ExecContext(
				ctx,
				`UPDATE secretary_jobs SET execution_blocked=TRUE WHERE source_id=$1`,
				job.SourceID,
			)
		}
		if err != nil {
			return domain.SecretaryAttempt{}, err
		}
		if err = failSecretaryOperationWorkTx(ctx, tx, job, "unknown", domain.ErrSecretaryExecutionUnknown); err != nil {
			return domain.SecretaryAttempt{}, err
		}
		if err = tx.Commit(); err != nil {
			return domain.SecretaryAttempt{}, err
		}
		return domain.SecretaryAttempt{}, domain.ErrSecretaryExecutionUnknown
	}
	id, err := newSecret("attempt")
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO secretary_attempts(attempt_id,batch_id,attempt_number,state) VALUES($1,$2,$3,'running')`,
		id,
		job.BatchID,
		batch.attemptCount+1,
	)
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_batches SET state='running',current_attempt_id=$2,attempt_count=attempt_count+1,updated_at=NOW() WHERE batch_id=$1`,
		job.BatchID,
		id,
	)
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	attempt, err := loadSecretaryAttempt(ctx, tx, id)
	if err != nil {
		return attempt, err
	}
	return attempt, tx.Commit()
}

func currentSecretaryAttempt(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	id string,
) (secretaryExecutionBatch, domain.SecretaryAttempt, error) {
	batch, err := loadSecretaryExecutionBatch(ctx, tx, job)
	if err != nil {
		return batch, domain.SecretaryAttempt{}, err
	}
	if batch.lease != job.LeaseToken || batch.attemptID != id {
		return batch, domain.SecretaryAttempt{}, domain.ErrSecretaryFenced
	}
	attempt, err := loadSecretaryAttempt(ctx, tx, id)
	return batch, attempt, err
}

func (s *PostgresStore) SaveSecretaryAttemptOutput(
	ctx context.Context,
	job domain.SecretaryJob,
	id, output string,
	reads domain.SecretaryToolReads,
) error {
	raw, err := secretaryOutputBytes(output, reads)
	if err != nil {
		return err
	}
	tx, err := secretaryExecutionTx(ctx, s, job)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, attempt, err := currentSecretaryAttempt(ctx, tx, job, id)
	if err != nil {
		return err
	}
	if attempt.State != "running" && attempt.State != "unknown" {
		return domain.ErrSecretaryFenced
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_attempts SET state='output_received',output_text=$2,reads_json=$3,updated_at=NOW() WHERE attempt_id=$1`,
		id,
		output,
		string(raw),
	)
	if err == nil {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_batches SET state='output_received',updated_at=NOW() WHERE batch_id=$1`,
			job.BatchID,
		)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) FailSecretaryAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
	id string,
	failure error,
) error {
	tx, err := secretaryExecutionTx(ctx, s, job)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	batch, attempt, err := currentSecretaryAttempt(ctx, tx, job, id)
	if err != nil {
		return err
	}
	if attempt.State != "running" && attempt.State != "output_received" &&
		attempt.State != "unknown" {
		return domain.ErrSecretaryFenced
	}
	if secretaryNotDispatched(failure) {
		if attempt.State != "running" {
			return domain.ErrSecretaryFenced
		}
		return failSecretaryConnectionTx(ctx, tx, job, batch, attempt, failure, s.now())
	}
	if attempt.State == "output_received" &&
		errors.Is(failure, domain.ErrSecretaryExecutionUnknown) {
		return tx.Commit()
	}
	state, batchState := secretaryAttemptFailure(
		failure,
		batch.attemptCount-batch.undispatchedAttempts,
	)
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_attempts SET state=$2,last_error=$3,updated_at=NOW() WHERE attempt_id=$1`,
		id,
		state,
		secretaryErrorText(failure),
	)
	if err == nil {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_batches SET state=$2,updated_at=NOW() WHERE batch_id=$1`,
			job.BatchID,
			batchState,
		)
	}
	if err == nil {
		err = failSecretaryAttemptJobTx(
			ctx,
			tx,
			job,
			batchState,
			failure,
			batch.attemptCount-batch.undispatchedAttempts,
		)
	}
	if err == nil {
		err = failSecretaryOperationWorkTx(ctx, tx, job, batchState, failure)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func readSecretaryBatchCommitTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	hash string,
) ([]domain.SummaryVersion, bool, error) {
	if job.BatchID == "" {
		return nil, false, nil
	}
	var storedHash, state string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT request_hash,results_json,state FROM secretary_batches WHERE batch_id=$1 AND owner_user_id=$2 AND source_id=$3`, job.BatchID, job.OwnerUserID, job.SourceID).
		Scan(&storedHash, &raw, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state != "committed" {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if hash != storedHash {
		return nil, false, domain.ErrConflict
	}
	var results []domain.SummaryVersion
	if err = json.Unmarshal(raw, &results); err != nil {
		return nil, false, err
	}
	for i, result := range results {
		results[i], err = getSummaryTx(ctx, tx, job.OwnerUserID, result.SummaryID, result.Revision)
		if err != nil {
			return nil, false, err
		}
	}
	return results, true, nil
}

func commitSecretaryBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	hash string,
	results []domain.SummaryVersion,
) error {
	if job.BatchID == "" {
		return nil
	}
	_, attempt, err := currentSecretaryAttempt(ctx, tx, job, job.AttemptID)
	if err != nil {
		return err
	}
	if attempt.State != "output_received" {
		return domain.ErrSecretaryFenced
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_batches SET state='committed',request_hash=$2,results_json=$3,updated_at=NOW() WHERE batch_id=$1`,
		job.BatchID,
		hash,
		string(raw),
	)
	if err == nil {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_attempts SET state='committed',updated_at=NOW() WHERE attempt_id=$1`,
			job.AttemptID,
		)
	}
	return err
}

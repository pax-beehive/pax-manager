package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func validateSecretaryBatchIdentity(stored, current domain.SecretaryJob) error {
	if !domain.SecretaryOutputPolicySupported(stored.OutputPolicyVersion) ||
		stored.BatchID != current.BatchID || stored.OwnerUserID != current.OwnerUserID ||
		stored.SourceID != current.SourceID || stored.SourceAgentID != current.SourceAgentID ||
		stored.SourceSessionID != current.SourceSessionID || stored.AgentID != current.AgentID ||
		stored.SettingsRevision != current.SettingsRevision || stored.RetryEpoch != current.RetryEpoch ||
		stored.ThroughRevision <= 0 || stored.ThroughRevision > current.ThroughRevision {
		return domain.ErrSecretaryFenced
	}
	return nil
}

func (s *MemoryStore) validateSecretaryBatchInputLocked(job domain.SecretaryJob) error {
	for _, message := range job.Messages {
		if _, err := s.readMemoryMessageVersionLocked(job.OwnerUserID, secretaryReference(message)); err != nil {
			return err
		}
	}
	for _, summary := range job.CurrentSummaries {
		if _, err := s.getMemorySummaryLocked(job.OwnerUserID, summary.SummaryID, summary.Revision, map[domain.SummaryRef]bool{}); err != nil {
			return err
		}
	}
	return nil
}

func validateSecretaryBatchInputTx(ctx context.Context, tx *sql.Tx, job domain.SecretaryJob) error {
	for _, message := range job.Messages {
		if _, err := readMessageVersionTx(ctx, tx, job.OwnerUserID, secretaryReference(message)); err != nil {
			return err
		}
	}
	for _, summary := range job.CurrentSummaries {
		if _, err := getSummaryTx(ctx, tx, job.OwnerUserID, summary.SummaryID, summary.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (s *MemoryStore) supersedeSecretaryBatchLocked(
	job domain.SecretaryJob,
	batch *memorySecretaryExecutionBatch,
	cause error,
) error {
	batch.state = "superseded"
	if len(batch.attempts) > 0 {
		attempt := &batch.attempts[len(batch.attempts)-1]
		attempt.State, attempt.UpdatedAt = "superseded", s.now().UTC()
	}
	queued := s.secretaryLocked().jobs[job.SourceID]
	queued.work.RetryEpoch++
	queued.executionBlocked = false
	return errors.Join(domain.ErrSourceVersionUnavailable, cause)
}

func supersedeSecretaryBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	cause error,
) error {
	_, err := tx.ExecContext(
		ctx,
		`UPDATE secretary_batches SET state='superseded',updated_at=NOW() WHERE batch_id=$1`,
		job.BatchID,
	)
	if err == nil {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_attempts SET state='superseded',updated_at=NOW()
        WHERE attempt_id=(SELECT current_attempt_id FROM secretary_batches WHERE batch_id=$1)`,
			job.BatchID,
		)
	}
	if err == nil {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET retry_epoch=retry_epoch+1,execution_blocked=FALSE WHERE source_id=$1`,
			job.SourceID,
		)
	}
	if err == nil {
		err = tx.Commit()
	}
	return errors.Join(domain.ErrSourceVersionUnavailable, cause, err)
}

func (s *MemoryStore) hasOpenSecretaryBatchLocked(
	source, owner string,
	settings, epoch int64,
) bool {
	for _, batch := range s.secretaryExecutionsLocked() {
		if batch.source == source && batch.owner == owner && batch.settings == settings &&
			batch.epoch == epoch &&
			secretaryBatchOpen(batch.state) {
			return true
		}
	}
	return false
}

func secretaryBatchOpen(state string) bool {
	return state == "pending" || state == "running" || state == "output_received" ||
		state == "unknown"
}

func hasOpenSecretaryBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secretary_batches WHERE source_id=$1 AND owner_user_id=$2
    AND settings_revision=$3 AND retry_epoch=$4 AND state IN ('pending','running','output_received','unknown'))`,
		job.SourceID, job.OwnerUserID, job.SettingsRevision, job.RetryEpoch).
		Scan(&open)
	return open, err
}

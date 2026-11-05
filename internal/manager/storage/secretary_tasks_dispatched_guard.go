package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryDispatchedInput(state string) bool {
	return state == "unknown" || state == "running"
}

func (s *MemoryStore) rejectSecretaryBatchInputLocked(
	job domain.SecretaryJob,
	batch *memorySecretaryExecutionBatch,
	failure error,
) error {
	if !secretaryDispatchedInput(batch.state) {
		return s.supersedeSecretaryBatchLocked(job, batch, failure)
	}
	queued := s.secretaryLocked().jobs[job.SourceID]
	queued.executionBlocked = true
	queued.work.LeaseToken, queued.leaseUntil = "", time.Time{}
	return ErrConflict
}

func rejectSecretaryBatchInputTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	batch secretaryExecutionBatch,
	failure error,
) error {
	if !secretaryDispatchedInput(batch.state) {
		return supersedeSecretaryBatchTx(ctx, tx, job, failure)
	}
	_, err := tx.ExecContext(ctx, `UPDATE secretary_jobs SET execution_blocked=TRUE,
 lease_token='',lease_until=NULL WHERE source_id=$1`, job.SourceID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return ErrConflict
}

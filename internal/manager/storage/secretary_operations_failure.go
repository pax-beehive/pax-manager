package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) recordSecretaryOperationJobFailure(
	ctx context.Context,
	job domain.SecretaryJob,
	failure error,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE secretary_operations o SET status='needs_attention',updated_at=NOW(),
 operation_json=operation_json||jsonb_build_object('status','needs_attention','error',$4::text,'updated_at',NOW())
 WHERE operation_id=$1 AND owner_user_id=$2 AND status IN ('queued','running')
 AND EXISTS(SELECT 1 FROM secretary_jobs j WHERE j.source_id=o.source_id AND j.owner_user_id=o.owner_user_id
 AND j.retry_epoch=$3 AND j.execution_blocked)`,
		job.OperationID,
		job.OwnerUserID,
		job.RetryEpoch,
		secretaryOperationErrorCode(failure),
	)
	return err
}

func (s *MemoryStore) failMemoryOperationWorkLocked(
	job domain.SecretaryJob,
	state string,
	failure error,
) {
	if job.OperationID == "" || state == "pending" {
		return
	}
	operation := s.secretaryOperationsLocked()[job.OperationID]
	if operation == nil || operation.owner != job.OwnerUserID ||
		operation.value.Status == "superseded" ||
		operation.value.Status == "completed" {
		return
	}
	s.failMemoryOperationLocked(s.secretaryLocked().jobs[job.SourceID], operation, failure)
}

func failSecretaryOperationWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	state string,
	failure error,
) error {
	if job.OperationID == "" || state == "pending" {
		return nil
	}
	operation, err := readSecretaryOperationTx(ctx, tx, job.OwnerUserID, job.OperationID, true)
	if err != nil {
		return err
	}
	if operation.value.Status == "superseded" || operation.value.Status == "completed" {
		return nil
	}
	operation.value.Status, operation.value.Error = "needs_attention", secretaryOperationErrorCode(
		failure,
	)
	return writeSecretaryOperationTx(ctx, tx, operation)
}

func (s *MemoryStore) retryMemoryOperationLocked(source string) {
	if s.activeMemoryOperationLocked(source) != nil {
		return
	}
	job := s.secretaryLocked().jobs[source]
	var selected *memorySecretaryOperation
	for _, operation := range s.secretaryOperationsLocked() {
		if job != nil && operation.owner == job.work.OwnerUserID &&
			operation.value.SourceID == source &&
			operation.value.Kind == "guided_summary" &&
			operation.value.Status == "needs_attention" &&
			(selected == nil || operation.value.UpdatedAt.After(selected.value.UpdatedAt) ||
				operation.value.UpdatedAt.Equal(selected.value.UpdatedAt) && operation.value.OperationID > selected.value.OperationID) {
			selected = operation
		}
	}
	if selected != nil {
		selected.value.Status, selected.value.Error = "queued", ""
		selected.value.UpdatedAt = s.now().UTC()
	}
}

func (s *MemoryStore) memoryOperationNeedsAttentionLocked(source string) bool {
	job := s.secretaryLocked().jobs[source]
	for _, operation := range s.secretaryOperationsLocked() {
		if job != nil && operation.owner == job.work.OwnerUserID &&
			operation.value.SourceID == source &&
			operation.value.Kind == "guided_summary" &&
			operation.value.Status == "needs_attention" {
			return true
		}
	}
	return false
}

func retrySecretaryOwnerOperationsTx(ctx context.Context, tx *sql.Tx, owner string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.source_id::text FROM secretary_operations o
 JOIN secretary_jobs j USING(source_id) WHERE o.owner_user_id=$1 AND j.owner_user_id=o.owner_user_id AND o.status='needs_attention'
 AND NOT j.paused AND (j.lease_token='' OR j.lease_until IS NULL OR j.lease_until<=NOW())`, owner)
	if err != nil {
		return err
	}
	var sources []string
	for rows.Next() {
		var source string
		if err = rows.Scan(&source); err != nil {
			_ = rows.Close()
			return err
		}
		sources = append(sources, source)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, source := range sources {
		if err = retrySecretaryOperationTx(ctx, tx, owner, source); err != nil {
			return err
		}
	}
	return nil
}

func retrySecretaryOperationTx(ctx context.Context, tx *sql.Tx, owner, source string) error {
	// An explicitly replaced operation remains superseded. Retry only the latest
	// unsuccessful guided operation when no other operation owns this source.
	var id string
	err := tx.QueryRowContext(ctx, `SELECT operation_id FROM secretary_operations o
 WHERE owner_user_id=$1 AND source_id=$2 AND kind='guided_summary' AND status='needs_attention'
 AND EXISTS(SELECT 1 FROM secretary_jobs owned WHERE owned.source_id=o.source_id AND owned.owner_user_id=o.owner_user_id)
 AND NOT EXISTS(SELECT 1 FROM secretary_operations active WHERE active.source_id=o.source_id
 AND active.status IN ('queued','running')) ORDER BY updated_at DESC,operation_id DESC LIMIT 1 FOR UPDATE`, owner, source).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	operation, err := readSecretaryOperationTx(ctx, tx, owner, id, false)
	if err != nil {
		return err
	}
	operation.value.Status, operation.value.Error = "queued", ""
	return writeSecretaryOperationTx(ctx, tx, operation)
}

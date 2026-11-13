package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) loadMemoryOperationWorkLocked(
	job *memorySecretaryJob,
	work domain.SecretaryJob,
	operation *memorySecretaryOperation,
) (domain.SecretaryJob, error) {
	work.OperationID, work.Instructions = operation.value.OperationID,
		cloneSecretaryOperation(operation.value).Instructions
	batch := newSecretaryBatch(&work)
	for _, target := range operation.value.Targets {
		if operation.completed[target.MessageID] {
			continue
		}
		doc, err := s.readSearchMessageLocked(work.OwnerUserID, target.MessageID)
		if err == nil && !secretaryOperationMessageMatches(work, target, doc) {
			err = secretaryOperationVersionError(target.MessageID)
		}
		if err == nil && len(doc.Body) > domain.SecretaryMaxSourceBytes {
			err = secretaryInputLimit("full-source byte limit")
		}
		if err != nil {
			s.failMemoryOperationLocked(job, operation, err)
			return work, err
		}
		consumed, err := batch.consume(
			secretaryChange{id: target.MessageID, revision: job.changes[target.MessageID]},
			doc,
			nil,
		)
		if err != nil {
			s.failMemoryOperationLocked(job, operation, err)
			return work, err
		}
		if !consumed || batch.full() {
			break
		}
	}
	if err := secretaryWithinBounds(work.Messages); err != nil {
		s.failMemoryOperationLocked(job, operation, err)
		return work, err
	}
	s.scopeMemoryOperationBatchLocked(&work)
	operation.value.Status, operation.value.Error = "running", ""
	operation.value.UpdatedAt = s.now().UTC()
	return work, nil
}

func secretaryOperationErrorCode(failure error) string {
	switch {
	case errors.Is(failure, domain.ErrSourceVersionUnavailable):
		return "source_changed"
	case errors.Is(failure, domain.ErrSummaryVersionMismatch):
		return "summary_changed"
	case errors.Is(failure, domain.ErrSecretaryExecutionUnknown):
		return "outcome_unknown"
	case secretaryInputBlocked(failure):
		return "input_size_limit"
	case domain.SecretaryValidationFailureCode(failure) != "":
		return "invalid_output"
	default:
		return "execution_failed"
	}
}

func (s *MemoryStore) failMemoryOperationLocked(job *memorySecretaryJob,
	operation *memorySecretaryOperation, failure error,
) {
	operation.value.Status, operation.value.Error = "needs_attention", secretaryOperationErrorCode(
		failure,
	)
	operation.value.UpdatedAt = s.now().UTC()
	job.executionBlocked = true
}

func (s *MemoryStore) finishMemoryOperationBatchLocked(
	job domain.SecretaryJob,
	results []domain.SummaryVersion,
) {
	if job.OperationID == "" {
		return
	}
	operation := s.secretaryOperationsLocked()[job.OperationID]
	if operation == nil || operation.owner != job.OwnerUserID {
		return
	}
	for _, message := range job.Messages {
		operation.completed[message.SourceID] = true
	}
	for _, ref := range secretarySummaryRefs(results) {
		found := false
		for _, previous := range operation.value.ResultRefs {
			found = found || previous == ref
		}
		if !found {
			operation.value.ResultRefs = append(operation.value.ResultRefs, ref)
		}
	}
	operation.value.Status = "queued"
	if len(operation.completed) == len(operation.value.Targets) {
		operation.value.Status = "completed"
	}
	operation.value.UpdatedAt = s.now().UTC()
}

func loadSecretaryOperationWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	job *domain.SecretaryJob,
) (bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT operation_id FROM secretary_operations
 WHERE source_id=$1 AND owner_user_id=$2 AND status IN ('queued','running') FOR UPDATE`,
		job.SourceID, job.OwnerUserID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	operation, err := readSecretaryOperationTx(ctx, tx, job.OwnerUserID, id, false)
	if err != nil {
		return false, err
	}
	job.OperationID, job.Instructions = id, operation.value.Instructions
	batch := newSecretaryBatch(job)
	for _, target := range operation.value.Targets {
		if operation.completed[target.MessageID] {
			continue
		}
		doc, readErr := loadSearchMessageWithByteLimit(
			ctx,
			tx,
			target.MessageID,
			domain.SecretaryMaxSourceBytes,
		)
		var revision int64
		if readErr == nil {
			readErr = tx.QueryRowContext(ctx, `SELECT change_revision FROM secretary_message_state WHERE source_id=$1 AND message_id=$2`,
				job.SourceID, target.MessageID).
				Scan(&revision)
		}
		if readErr == nil && !secretaryOperationMessageMatches(*job, target, doc) {
			readErr = secretaryOperationVersionError(target.MessageID)
		}
		if readErr != nil {
			return true, blockSecretaryOperationTx(ctx, tx, operation, readErr)
		}
		consumed, consumeErr := batch.consume(
			secretaryChange{id: target.MessageID, revision: revision},
			doc,
			nil,
		)
		if consumeErr != nil {
			return true, blockSecretaryOperationTx(ctx, tx, operation, consumeErr)
		}
		if !consumed || batch.full() {
			break
		}
	}
	if err = secretaryWithinBounds(job.Messages); err != nil {
		return true, blockSecretaryOperationTx(ctx, tx, operation, err)
	}
	if err = scopeSecretaryOperationBatchTx(ctx, tx, job); err != nil {
		return true, err
	}
	operation.value.Status, operation.value.Error = "running", ""
	if err = writeSecretaryOperationTx(ctx, tx, operation); err != nil {
		return true, err
	}
	return true, nil
}

func blockSecretaryOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	operation *memorySecretaryOperation,
	failure error,
) error {
	operation.value.Status, operation.value.Error = "needs_attention", secretaryOperationErrorCode(
		failure,
	)
	if err := writeSecretaryOperationTx(ctx, tx, operation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE secretary_jobs SET execution_blocked=TRUE WHERE source_id=$1`, operation.value.SourceID); err != nil {
		return err
	}
	return failure
}

func finishSecretaryOperationBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	results []domain.SummaryVersion,
) error {
	if job.OperationID == "" {
		return nil
	}
	operation, err := readSecretaryOperationTx(ctx, tx, job.OwnerUserID, job.OperationID, true)
	if err != nil {
		return err
	}
	for _, message := range job.Messages {
		operation.completed[message.SourceID] = true
	}
	for _, ref := range secretarySummaryRefs(results) {
		found := false
		for _, previous := range operation.value.ResultRefs {
			found = found || previous == ref
		}
		if !found {
			operation.value.ResultRefs = append(operation.value.ResultRefs, ref)
		}
	}
	operation.value.Status = "queued"
	if len(operation.completed) == len(operation.value.Targets) {
		operation.value.Status = "completed"
	}
	return writeSecretaryOperationTx(ctx, tx, operation)
}

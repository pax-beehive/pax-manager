package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) SubmitSecretaryOperation(ctx context.Context, owner, id string,
	input domain.SecretaryOperationSubmit,
) (domain.SecretaryOperation, error) {
	if owner == "" {
		return domain.SecretaryOperation{}, ErrUnauthorized
	}
	for range 3 {
		result, err := s.submitSecretaryOperation(ctx, owner, id, input)
		if !summaryRetryable(err) {
			return result, err
		}
	}
	return domain.SecretaryOperation{}, ErrConflict
}

func (s *PostgresStore) submitSecretaryOperation(ctx context.Context, owner, id string,
	input domain.SecretaryOperationSubmit,
) (domain.SecretaryOperation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := readSecretaryOperationTx(ctx, tx, owner, id, false)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	job, err := lockSecretaryOperationSourceTx(ctx, tx, owner, stored.value.SourceID)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"pax:secretary:operation:"+owner+":"+input.RequestID); err != nil {
		return domain.SecretaryOperation{}, err
	}
	stored, err = readSecretaryOperationTx(ctx, tx, owner, id, true)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if err = validateSecretaryOperationAccessTx(ctx, tx, owner, stored.value); err != nil {
		return domain.SecretaryOperation{}, err
	}
	hash := secretaryOperationHash(input)
	var previousID, previousHash string
	err = tx.QueryRowContext(ctx, `SELECT operation_id,request_hash FROM secretary_operations
 WHERE owner_user_id=$1 AND request_id=$2`, owner, input.RequestID).Scan(&previousID, &previousHash)
	if err == nil {
		if previousID != id || previousHash != hash {
			return domain.SecretaryOperation{}, ErrConflict
		}
		return stored.value, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.SecretaryOperation{}, err
	}
	if stored.value.Status != "draft" || stored.value.Revision != input.ExpectedRevision {
		return domain.SecretaryOperation{}, ErrConflict
	}
	operation := cloneSecretaryOperation(stored.value)
	operation.Targets, err = secretaryOperationTargets(operation, input)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if err = validateSecretaryOperationSubmitTx(ctx, tx, job, operation, input); err != nil {
		return domain.SecretaryOperation{}, err
	}
	result, err := s.applySecretaryOperationSubmitTx(ctx, tx, job, stored, operation, input, hash)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) applySecretaryOperationSubmitTx(ctx context.Context, tx *sql.Tx,
	job domain.SecretaryJob, stored *memorySecretaryOperation, operation domain.SecretaryOperation,
	input domain.SecretaryOperationSubmit, hash string,
) (domain.SecretaryOperation, error) {
	owner, id := job.OwnerUserID, operation.OperationID
	var err error
	if operation.Instructions == nil {
		operation.Instructions, err = loadSecretaryInstructionsTx(
			ctx,
			tx,
			owner,
			operation.SourceID,
		)
		if err != nil {
			return domain.SecretaryOperation{}, err
		}
	}
	operation.Instructions.Run, err = domain.NormalizeSecretaryInstruction(input.RunInstruction)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	var results []domain.SummaryVersion
	if operation.Kind != "guided_summary" {
		result, publishErr := s.publishHumanSummaryTx(ctx, tx, owner, operation, input)
		if publishErr != nil {
			return domain.SecretaryOperation{}, publishErr
		}
		results = append(results, result)
	}
	if err = supersedeSecretaryOperationWorkTx(ctx, tx, job, id); err != nil {
		return domain.SecretaryOperation{}, err
	}
	operation.Revision++
	operation.UpdatedAt = s.now().UTC()
	if operation.Kind == "guided_summary" {
		operation.Status = "queued"
	} else {
		operation.Status, operation.ResultRefs = "completed", secretarySummaryRefs(results)
		if err = acknowledgeSecretaryOperationInvalidationsTx(ctx, tx, job, operation); err != nil {
			return domain.SecretaryOperation{}, err
		}
		if err = acknowledgeSecretaryOperationTx(ctx, tx, job, operation.Targets); err != nil {
			return domain.SecretaryOperation{}, err
		}
	}
	stored.value, stored.requestID, stored.hash = operation, input.RequestID, hash
	if err = writeSecretaryOperationTx(ctx, tx, stored); err != nil {
		return domain.SecretaryOperation{}, err
	}
	return stored.value, nil
}

func validateSecretaryOperationSubmitTx(ctx context.Context, tx *sql.Tx, job domain.SecretaryJob,
	operation domain.SecretaryOperation, input domain.SecretaryOperationSubmit,
) error {
	if operation.Kind == "guided_summary" {
		var available bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secretary_settings cfg
 JOIN agents a ON a.agent_id=cfg.agent_id AND a.owner_user_id=cfg.owner_user_id AND a.deleted_at IS NULL
 LEFT JOIN nodes n ON n.node_id=a.node_id WHERE cfg.owner_user_id=$1 AND cfg.enabled AND n.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM secretary_jobs j WHERE j.source_id=$2 AND j.paused))`, job.OwnerUserID, job.SourceID).Scan(&available)
		if err != nil {
			return err
		}
		if !available {
			return ErrConflict
		}
	}
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT (lease_token<>'' AND lease_until>NOW())
 OR EXISTS(SELECT 1 FROM secretary_batches b WHERE b.source_id=j.source_id AND b.owner_user_id=j.owner_user_id
 AND b.retry_epoch=j.retry_epoch AND b.state IN ('pending','running','output_received','unknown'))
 OR EXISTS(SELECT 1 FROM secretary_operations o WHERE o.source_id=j.source_id AND o.owner_user_id=j.owner_user_id AND o.status IN ('queued','running'))
 FROM secretary_jobs j WHERE j.source_id=$1`, job.SourceID).Scan(&active)
	if err != nil {
		return err
	}
	if active && !input.ConfirmReplace {
		return fmtSecretaryReplacementRequired()
	}
	if err = validateSecretaryOperationBaselineTx(ctx, tx, job, operation); err != nil {
		return err
	}
	return validateSecretaryOperationTargetsTx(ctx, tx, job, operation.Targets)
}

func validateSecretaryOperationBaselineTx(ctx context.Context, tx *sql.Tx,
	job domain.SecretaryJob, operation domain.SecretaryOperation,
) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM knowledge_nodes
 WHERE owner_user_id=$1 AND origin_source_id=$2 AND type='summary'`, job.OwnerUserID, job.SourceID).Scan(&count); err != nil {
		return err
	}
	if count != len(operation.BaselineRefs) {
		return domain.ErrSummaryVersionMismatch
	}
	locks := make([]domain.PublishSummaryRequest, 0, len(operation.BaselineRefs))
	for _, ref := range operation.BaselineRefs {
		locks = append(locks, domain.PublishSummaryRequest{SummaryID: ref.SummaryID})
	}
	if err := lockSecretaryNodeIDsTx(ctx, tx, locks); err != nil {
		return err
	}
	for _, ref := range operation.BaselineRefs {
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT current_revision FROM knowledge_nodes WHERE id=$1 AND owner_user_id=$2`,
			ref.SummaryID, job.OwnerUserID).
			Scan(&revision)
		if err != nil {
			return err
		}
		if revision != ref.Revision {
			return domain.ErrSummaryVersionMismatch
		}
	}
	return nil
}

func validateSecretaryOperationTargetsTx(ctx context.Context, tx *sql.Tx,
	job domain.SecretaryJob, targets []domain.SecretaryOperationTarget,
) error {
	for _, target := range targets {
		doc, readErr := loadSearchMessage(ctx, tx, target.MessageID)
		if readErr != nil {
			return readErr
		}
		if !secretaryOperationMessageMatches(job, target, doc) {
			return secretaryOperationVersionError(target.MessageID)
		}
		job.Messages = append(job.Messages, domain.SearchDocument{SourceID: doc.SourceID})
	}
	complete, err := secretarySourcesCompleteTx(ctx, tx, job)
	if err != nil {
		return err
	}
	if !complete {
		return domain.ErrSecretarySourceIncomplete
	}
	return nil
}

func (s *PostgresStore) publishHumanSummaryTx(ctx context.Context, tx *sql.Tx, owner string,
	operation domain.SecretaryOperation, input domain.SecretaryOperationSubmit,
) (domain.SummaryVersion, error) {
	request, err := secretaryOperationSummaryRequest(operation, input)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	if err = validateSummaryHead(ctx, tx, owner, request); err != nil {
		return domain.SummaryVersion{}, err
	}
	var frozen []domain.SearchDocument
	for _, ref := range request.Coverage {
		doc, readErr := readMessageVersionTx(ctx, tx, owner, ref)
		if readErr != nil {
			return domain.SummaryVersion{}, readErr
		}
		frozen = append(frozen, doc)
	}
	for _, ref := range request.SummarySources {
		if _, err = getSummaryTx(ctx, tx, owner, ref.SummaryID, ref.Revision); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	result := domain.SummaryVersion{
		SummaryID:      request.SummaryID,
		Revision:       request.ExpectedRevision + 1,
		OwnerUserID:    owner,
		SubjectType:    request.SubjectType,
		SubjectID:      request.SubjectID,
		Title:          request.Title,
		Body:           request.Body,
		Coverage:       request.Coverage,
		SummarySources: request.SummarySources,
		AuthorKind:     "user",
		AuthorID:       owner,
		OperationID:    operation.OperationID,
		CreatedAt:      s.now().UTC(),
	}
	if err = insertSummaryTx(ctx, tx, request, secretaryRequestHash(request), result, frozen); err != nil {
		return domain.SummaryVersion{}, err
	}
	return getSummaryTx(ctx, tx, owner, result.SummaryID, result.Revision)
}

func supersedeSecretaryOperationWorkTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	replacement string,
) error {
	_, err := tx.ExecContext(
		ctx,
		`UPDATE secretary_attempts a SET state='superseded',updated_at=NOW()
 FROM secretary_batches b WHERE a.batch_id=b.batch_id AND b.source_id=$1 AND b.owner_user_id=$2
 AND a.state IN ('running','unknown','output_received')`,
		job.SourceID,
		job.OwnerUserID,
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_batches SET state='superseded',lease_token='',updated_at=NOW()
 WHERE source_id=$1 AND owner_user_id=$2 AND state<>'committed'`,
		job.SourceID,
		job.OwnerUserID,
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_operations SET status='superseded',revision=revision+1,updated_at=NOW(),
 operation_json=operation_json||jsonb_build_object('status','superseded','revision',revision+1,'updated_at',NOW())
 WHERE source_id=$1 AND operation_id<>$2 AND status IN ('queued','running','needs_attention')`,
		job.SourceID,
		replacement,
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET retry_epoch=retry_epoch+1,retry_generation=retry_generation+1,
 lease_token='',lease_until=NULL,execution_blocked=FALSE,control_revision=control_revision+1,
 last_error='',last_validation_failure='',auto_retry_key='',auto_retry_count=0,available_at=NOW(),
 updated_at=NOW()-INTERVAL '30 seconds' WHERE source_id=$1`,
		job.SourceID,
	)
	return err
}

func acknowledgeSecretaryOperationTx(ctx context.Context, tx *sql.Tx, job domain.SecretaryJob,
	targets []domain.SecretaryOperationTarget,
) error {
	for _, target := range targets {
		outcome, _ := json.Marshal(domain.SecretaryMessageOutcome{MessageID: target.MessageID,
			Action: "no_summary", Reason: "Completed by user summary operation"})
		_, err := tx.ExecContext(ctx, `UPDATE secretary_message_state SET processed_hash=$3,
 processed_canonicalization_version=$4,processed_owner_user_id=$5,processed_change_revision=change_revision,
 processing_result=$7::jsonb WHERE source_id=$1 AND message_id=$2 AND change_revision>=$6`,
			job.SourceID, target.MessageID, target.ContentHash, target.CanonicalizationVersion,
			job.OwnerUserID, target.ChangeRevision, string(outcome))
		if err != nil {
			return err
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return advanceSecretaryCursorTx(ctx, tx, job.SourceID)
}

func advanceSecretaryCursorTx(ctx context.Context, tx *sql.Tx, source string) error {
	_, err := tx.ExecContext(
		ctx,
		secretaryCursorAdvanceSQL,
		source,
		secretaryProgressWhitespace,
	)
	return err
}

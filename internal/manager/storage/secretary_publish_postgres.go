package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) PublishSecretaryJob(
	ctx context.Context,
	job domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	request, err := secretaryComposePublication(job, request)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	for range 3 {
		result, err := s.publishSecretaryJob(ctx, job, request)
		if !summaryRetryable(err) {
			return result, err
		}
	}
	return domain.SummaryVersion{}, ErrConflict
}

func (s *PostgresStore) publishSecretaryJob(
	ctx context.Context,
	job domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockSecretaryJob(ctx, tx, job); err != nil {
		return domain.SummaryVersion{}, err
	}
	request = summaryWithoutReceipts(request)
	hash := secretaryRequestHash(request)
	for _, key := range []string{"pax:summary:request:" + job.OwnerUserID + ":" + request.RequestID, "pax:summary:" + request.SummaryID} {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	result, err := insertSecretarySummary(ctx, tx, job, request, hash, s.now().UTC())
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	for _, message := range job.Messages {
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_message_state SET processed_hash=$3,processed_canonicalization_version=$4,processed_owner_user_id=$5
	 WHERE source_id=$1 AND message_id=$2`,
			job.SourceID,
			message.SourceID,
			message.ContentHash,
			message.CanonicalizationVersion,
			job.OwnerUserID,
		)
		if err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	for _, id := range job.RemovedMessageIDs {
		if _, err = tx.ExecContext(ctx, `UPDATE secretary_message_state SET processed_hash='',processed_canonicalization_version='',processed_owner_user_id='' WHERE source_id=$1 AND message_id=$2`, job.SourceID, id); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET processed_revision=GREATEST(processed_revision,$2),
 lease_token='',lease_until=NULL,completed_at=NOW(),last_error='',attempts=0,available_at=NOW()
 WHERE source_id=$1`,
		job.SourceID,
		job.ThroughRevision,
	)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	return result, tx.Commit()
}

func lockSecretaryJob(ctx context.Context, tx *sql.Tx, job domain.SecretaryJob) error {
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM secretary_settings WHERE owner_user_id=$1 FOR UPDATE`, job.OwnerUserID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrSecretaryFenced
		}
		return err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM secretary_jobs WHERE source_id=$1 FOR UPDATE`, job.SourceID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrSecretaryFenced
		}
		return err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, secretaryActiveSQL, job.SourceID, job.OwnerUserID, job.AgentID, job.SettingsRevision, job.LeaseToken).Scan(&active); err != nil {
		return err
	}
	if !active || job.ThroughRevision <= 0 || job.ThroughRevision > revision {
		return domain.ErrSecretaryFenced
	}
	return nil
}

func insertSecretarySummary(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	request domain.PublishSummaryRequest,
	hash string,
	createdAt time.Time,
) (domain.SummaryVersion, error) {
	var existingID, existingHash string
	var existingRevision int64
	err := tx.QueryRowContext(ctx, `SELECT summary_id,revision,request_hash FROM summary_versions WHERE owner_user_id=$1 AND request_id=$2`, job.OwnerUserID, request.RequestID).
		Scan(&existingID, &existingRevision, &existingHash)
	if err == nil {
		if existingHash != hash {
			return domain.SummaryVersion{}, ErrConflict
		}
		return getSummaryTx(ctx, tx, job.OwnerUserID, existingID, existingRevision)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.SummaryVersion{}, err
	}
	if err = validateSummaryHead(ctx, tx, job.OwnerUserID, request); err != nil {
		return domain.SummaryVersion{}, err
	}
	var frozen []domain.SearchDocument
	for _, ref := range request.Coverage {
		doc, readErr := readMessageVersionTx(ctx, tx, job.OwnerUserID, ref)
		if readErr != nil {
			return domain.SummaryVersion{}, readErr
		}
		for _, required := range job.Messages {
			if secretaryReference(required).Identity() == ref.Identity() &&
				(doc.AgentID != job.SourceAgentID || doc.SessionID != job.SourceSessionID) {
				return domain.SummaryVersion{}, ErrConflict
			}
		}
		frozen = append(frozen, doc)
	}
	if err = validateSummaryQuotes(request, frozen); err != nil {
		return domain.SummaryVersion{}, err
	}
	for _, ref := range request.SummarySources {
		if _, err = getSummaryTx(ctx, tx, job.OwnerUserID, ref.SummaryID, ref.Revision); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	result := domain.SummaryVersion{
		SummaryID:      request.SummaryID,
		Revision:       request.ExpectedRevision + 1,
		OwnerUserID:    job.OwnerUserID,
		SubjectType:    request.SubjectType,
		SubjectID:      request.SubjectID,
		Title:          request.Title,
		Body:           request.Body,
		Claims:         request.Claims,
		Coverage:       request.Coverage,
		SummarySources: request.SummarySources,
		CreatedAt:      createdAt,
	}
	if err = insertSummaryTx(ctx, tx, request, hash, result, frozen); err != nil {
		return domain.SummaryVersion{}, err
	}
	if err = validateSummaryAccess(ctx, tx, job.OwnerUserID, result.SummaryID, result.Revision); err != nil {
		return domain.SummaryVersion{}, err
	}
	return result, nil
}

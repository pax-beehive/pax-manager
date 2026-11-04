package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) PublishSecretaryBatch(
	ctx context.Context, job domain.SecretaryJob, publication domain.SecretaryBatchPublication,
) ([]domain.SummaryVersion, error) {
	requests, hash, err := secretaryNodePublication(job, publication)
	if err != nil {
		return nil, err
	}
	for range 3 {
		results, publishErr := s.publishSecretaryNodes(
			ctx,
			job,
			requests,
			hash,
			publication.Outcomes,
		)
		if !summaryRetryable(publishErr) {
			return results, publishErr
		}
	}
	return nil, ErrConflict
}

func (s *PostgresStore) publishSecretaryNodes(
	ctx context.Context,
	job domain.SecretaryJob,
	requests []domain.PublishSummaryRequest,
	hash string,
	outcomes []domain.SecretaryMessageOutcome,
) ([]domain.SummaryVersion, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "pax:secretary:batch:"+job.BatchID); err != nil {
		return nil, err
	}
	if results, committed, readErr := readSecretaryBatchCommitTx(ctx, tx, job, hash); readErr != nil ||
		committed {
		if readErr != nil {
			return nil, readErr
		}
		return results, tx.Commit()
	}
	if err = lockSecretaryJob(ctx, tx, job); err != nil {
		return nil, err
	}
	if err = validateSecretaryNodeInputsTx(ctx, tx, job); err != nil {
		return nil, err
	}
	if err = validateSecretaryNodeSourcesCompleteTx(ctx, tx, job.OwnerUserID, requests); err != nil {
		return nil, err
	}
	if err = lockSecretaryFrozenHeadsTx(ctx, tx, job, requests); err != nil {
		return nil, err
	}
	results, err := s.publishSecretaryNodeOutputsTx(ctx, tx, job, requests)
	if err != nil {
		return nil, err
	}
	if err = validateSecretaryNodeBudgetTx(ctx, tx, results); err != nil {
		return nil, err
	}
	if err = commitSecretaryBatchTx(ctx, tx, job, hash, results); err != nil {
		return nil, err
	}
	if err = checkpointSecretaryNodesTx(ctx, tx, job, outcomes); err != nil {
		return nil, err
	}
	if err = clearSecretaryInvalidationsTx(ctx, tx, job, results); err != nil {
		return nil, err
	}
	return results, tx.Commit()
}

func lockSecretaryNodeIDsTx(
	ctx context.Context,
	tx *sql.Tx,
	requests []domain.PublishSummaryRequest,
) error {
	var err error
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.SummaryID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "pax:summary:"+id); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) publishSecretaryNodeOutputsTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	requests []domain.PublishSummaryRequest,
) ([]domain.SummaryVersion, error) {
	results := make([]domain.SummaryVersion, 0, len(requests))
	for _, request := range requests {
		var owner, nodeType, origin string
		readErr := tx.QueryRowContext(ctx, `SELECT owner_user_id,type,origin_source_id FROM knowledge_nodes WHERE id=$1`, request.SummaryID).
			Scan(&owner, &nodeType, &origin)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return nil, readErr
		}
		if readErr == nil &&
			(owner != job.OwnerUserID || nodeType != "summary" || origin != job.SourceID) {
			return nil, ErrNotFound
		}
		result, insertErr := insertSecretarySummary(
			ctx,
			tx,
			job,
			request,
			secretaryRequestHash(request),
			s.now().UTC(),
		)
		if insertErr != nil {
			return nil, insertErr
		}
		result, insertErr = getSummaryTx(
			ctx,
			tx,
			job.OwnerUserID,
			result.SummaryID,
			result.Revision,
		)
		if insertErr != nil {
			return nil, insertErr
		}
		results = append(results, result)
	}
	return results, nil
}

func validateSecretaryNodeInputsTx(ctx context.Context, tx *sql.Tx, job domain.SecretaryJob) error {
	for _, expected := range job.Messages {
		current, err := loadSearchMessage(ctx, tx, expected.SourceID)
		if err != nil {
			return err
		}
		if current.OwnerUserID != job.OwnerUserID || current.AgentID != job.SourceAgentID ||
			current.SessionID != job.SourceSessionID {
			return ErrNotFound
		}
		if current.ContentHash != expected.ContentHash ||
			current.CanonicalizationVersion != expected.CanonicalizationVersion {
			return domain.ErrSourceVersionUnavailable
		}
	}
	return nil
}

func checkpointSecretaryNodesTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
	outcomes []domain.SecretaryMessageOutcome,
) error {
	for _, message := range job.Messages {
		if _, err := tx.ExecContext(ctx, `UPDATE secretary_message_state SET processed_hash=$3,
 processed_canonicalization_version=$4,processed_owner_user_id=$5 WHERE source_id=$1 AND message_id=$2`,
			job.SourceID, message.SourceID, message.ContentHash, message.CanonicalizationVersion, job.OwnerUserID); err != nil {
			return err
		}
	}
	for _, id := range job.RemovedMessageIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE secretary_message_state SET processed_hash='',
 processed_canonicalization_version='',processed_owner_user_id='' WHERE source_id=$1 AND message_id=$2`, job.SourceID, id); err != nil {
			return err
		}
	}
	for _, outcome := range outcomes {
		raw, _ := json.Marshal(outcome)
		if _, err := tx.ExecContext(ctx, `UPDATE secretary_message_state SET last_batch_id=$3,
 processing_result=$4::jsonb WHERE source_id=$1 AND message_id=$2`, job.SourceID, outcome.MessageID, job.BatchID, string(raw)); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(
		ctx,
		`UPDATE secretary_jobs SET processed_revision=GREATEST(processed_revision,$2),
 lease_token='',lease_until=NULL,execution_blocked=FALSE,completed_at=NOW(),last_error='',attempts=0,available_at=NOW(),
 auto_retry_key='',auto_retry_count=0,last_validation_failure='' WHERE source_id=$1`,
		job.SourceID,
		job.ThroughRevision,
	)
	return err
}

func validateSecretaryNodeBudgetTx(
	ctx context.Context,
	tx *sql.Tx,
	results []domain.SummaryVersion,
) error {
	if len(results) == 0 {
		return nil
	}
	raw, _ := json.Marshal(secretarySummaryRefs(results))
	var sources, dependencies, bytes int64
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE dependencies(summary_id,revision) AS (
 SELECT summary_id,revision FROM jsonb_to_recordset($1::jsonb) AS input(summary_id text,revision bigint)
 UNION SELECT src.child_summary_id,src.child_revision FROM summary_sources src JOIN dependencies d USING(summary_id,revision)
 WHERE src.source_kind='summary'
), bounded AS MATERIALIZED (SELECT * FROM dependencies LIMIT 4097), leaves AS (
 SELECT DISTINCT src.message_id,src.canonicalization_version,src.content_hash FROM summary_sources src
 JOIN bounded d USING(summary_id,revision) WHERE src.source_kind='message'
) SELECT (SELECT count(*) FROM leaves),(SELECT count(*) FROM bounded),
 COALESCE((SELECT sum(octet_length(snapshot.body)) FROM leaves JOIN message_snapshots snapshot
 USING(message_id,canonicalization_version,content_hash)),0)`, string(raw)).Scan(&sources, &dependencies, &bytes)
	if err != nil {
		return err
	}
	if sources > secretaryCoverageLimit || dependencies > secretaryCoverageLimit ||
		bytes > domain.SecretaryMaxSourceBytes {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) ListSecretaryNodeSummaries(
	ctx context.Context, owner, source string, query domain.SummaryListQuery,
) (domain.SecretarySummaryPage, error) {
	if owner == "" {
		return domain.SecretarySummaryPage{}, ErrUnauthorized
	}
	after, limit, err := summaryListQuery(owner+":"+source, query)
	if err != nil {
		return domain.SecretarySummaryPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretarySummaryPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	origin, err := readSessionSearchGroupTx(ctx, tx, owner, source, "", "")
	if err != nil {
		return domain.SecretarySummaryPage{}, err
	}
	refs, err := listSecretaryNodeRefs(ctx, tx, owner, source, after, limit+1)
	if err != nil {
		return domain.SecretarySummaryPage{}, err
	}
	page := domain.SecretarySummaryPage{Items: []domain.SummaryVersion{}}
	if len(refs) > limit {
		page.NextCursor = summaryNextCursor(owner+":"+source, refs[limit-1].SummaryID)
		refs = refs[:limit]
	}
	for _, ref := range refs {
		current, readErr := getSummaryTx(ctx, tx, owner, ref.SummaryID, ref.Revision)
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return domain.SecretarySummaryPage{}, readErr
		}
		current.OriginSourceID, current.OriginAgentID, current.OriginSessionID, current.OriginSessionTitle = source, origin.AgentID, origin.SessionID, origin.Title
		page.Items = append(page.Items, current)
	}
	return page, tx.Commit()
}

func listSecretaryNodeRefs(
	ctx context.Context, tx *sql.Tx, owner, source, after string, limit int,
) ([]domain.SummaryRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,current_revision FROM knowledge_nodes
 WHERE owner_user_id=$1 AND origin_source_id=$2 AND type='summary' AND id>$3 ORDER BY id LIMIT $4`,
		owner, source, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	refs := make([]domain.SummaryRef, 0, limit)
	for rows.Next() {
		var ref domain.SummaryRef
		if err = rows.Scan(&ref.SummaryID, &ref.Revision); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func markSecretaryNodeChangesTx(ctx context.Context, tx *sql.Tx, job *domain.SecretaryJob) error {
	changes := make([]domain.MessageVersionRef, 0, len(job.Messages)+len(job.RemovedMessageIDs))
	for _, message := range job.Messages {
		changes = append(changes, secretaryReference(message))
	}
	for _, id := range job.RemovedMessageIDs {
		changes = append(changes, domain.MessageVersionRef{MessageID: id})
	}
	if len(changes) == 0 {
		return nil
	}
	raw, _ := json.Marshal(changes)
	rows, err := tx.QueryContext(
		ctx,
		`SELECT DISTINCT node.id,node.current_revision FROM knowledge_nodes node
 JOIN summary_sources src ON src.summary_id=node.id AND src.revision=node.current_revision
 JOIN jsonb_to_recordset($3::jsonb) AS change(message_id text,content_hash text,canonicalization_version text)
 ON change.message_id=src.message_id WHERE node.owner_user_id=$1 AND node.origin_source_id=$2
 AND node.type='summary' AND (src.content_hash<>change.content_hash OR src.canonicalization_version<>change.canonicalization_version)
 ORDER BY node.id`,
		job.OwnerUserID,
		job.SourceID,
		string(raw),
	)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ref domain.SummaryRef
		if err = rows.Scan(&ref.SummaryID, &ref.Revision); err != nil {
			return err
		}
		secretaryAddInvalidated(job, ref)
	}
	return rows.Err()
}

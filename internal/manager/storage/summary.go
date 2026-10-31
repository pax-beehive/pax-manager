package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SummaryStore = (*PostgresStore)(nil)

const summarySelectSQL = `SELECT summary_id, revision, owner_user_id, subject_type, subject_id,
    title, body, claims_json, created_at FROM summary_versions`

func (s *PostgresStore) PublishSummary(
	ctx context.Context, owner string, request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	if owner == "" {
		return domain.SummaryVersion{}, ErrUnauthorized
	}
	if err := request.Validate(); err != nil {
		return domain.SummaryVersion{}, err
	}
	request = summaryWithoutReceipts(request)
	payload, err := json.Marshal(request)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	requestHash := domain.SearchContentHash(string(payload))
	for attempt := 0; attempt < 3; attempt++ {
		result, publishErr := s.publishSummary(ctx, owner, request, requestHash)
		if !summaryRetryable(publishErr) {
			return result, publishErr
		}
	}
	return domain.SummaryVersion{}, ErrConflict
}

func (s *PostgresStore) publishSummary(
	ctx context.Context, owner string, request domain.PublishSummaryRequest, requestHash string,
) (domain.SummaryVersion, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range []string{"pax:summary:request:" + owner + ":" + request.RequestID, "pax:summary:" + request.SummaryID} {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	var existingID, existingHash string
	var existingRevision int64
	err = tx.QueryRowContext(ctx, `SELECT summary_id, revision, request_hash FROM summary_versions
        WHERE owner_user_id = $1 AND request_id = $2`, owner, request.RequestID).
		Scan(&existingID, &existingRevision, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			return domain.SummaryVersion{}, ErrConflict
		}
		result, readErr := getSummaryTx(ctx, tx, owner, existingID, existingRevision)
		if readErr != nil {
			return domain.SummaryVersion{}, readErr
		}
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.SummaryVersion{}, err
	}
	if err = validateSummaryHead(ctx, tx, owner, request); err != nil {
		return domain.SummaryVersion{}, err
	}
	frozen := make([]domain.SearchDocument, 0, len(request.Coverage))
	for _, ref := range request.Coverage {
		doc, readErr := readMessageVersionTx(ctx, tx, owner, ref)
		if readErr != nil {
			return domain.SummaryVersion{}, readErr
		}
		frozen = append(frozen, doc)
	}
	if err = validateSummaryQuotes(request, frozen); err != nil {
		return domain.SummaryVersion{}, err
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
		Claims:         request.Claims,
		Coverage:       request.Coverage,
		SummarySources: request.SummarySources,
		CreatedAt:      s.now().UTC(),
	}
	if err = insertSummaryTx(ctx, tx, request, requestHash, result, frozen); err != nil {
		return domain.SummaryVersion{}, err
	}
	return result, tx.Commit()
}

func validateSummaryHead(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	request domain.PublishSummaryRequest,
) error {
	var storedOwner, subjectType, subjectID string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT owner_user_id, subject_type, subject_id, revision
        FROM summary_versions WHERE summary_id = $1 ORDER BY revision DESC LIMIT 1`, request.SummaryID).
		Scan(&storedOwner, &subjectType, &subjectID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		if request.ExpectedRevision != 0 {
			return domain.ErrSummaryVersionMismatch
		}
		return nil
	}
	if err != nil {
		return err
	}
	if storedOwner != owner {
		return ErrNotFound
	}
	if subjectType != request.SubjectType || subjectID != request.SubjectID {
		return ErrConflict
	}
	if revision != request.ExpectedRevision {
		return domain.ErrSummaryVersionMismatch
	}
	return nil
}

func insertSummaryTx(ctx context.Context, tx *sql.Tx, request domain.PublishSummaryRequest,
	requestHash string, result domain.SummaryVersion, frozen []domain.SearchDocument,
) error {
	claims, err := json.Marshal(result.Claims)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO summary_versions (summary_id, revision, owner_user_id,
        subject_type, subject_id, title, body, claims_json, request_id, request_hash, created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, result.SummaryID, result.Revision,
		result.OwnerUserID, result.SubjectType, result.SubjectID, result.Title, result.Body,
		claims, request.RequestID, requestHash, result.CreatedAt)
	if err != nil {
		return err
	}
	for i, doc := range frozen {
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO message_snapshots (message_id, canonicalization_version,
            content_hash, owner_user_id, agent_id, session_id, conversation_id, role, session_seq,
            message_created_at, body, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
            ON CONFLICT (message_id, canonicalization_version, content_hash) DO NOTHING`,
			doc.SourceID,
			doc.CanonicalizationVersion,
			doc.ContentHash,
			result.OwnerUserID,
			doc.AgentID,
			doc.SessionID,
			doc.ConversationID,
			doc.Role,
			doc.SessionSeq,
			doc.CreatedAt,
			doc.Body,
			result.CreatedAt,
		)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO summary_sources (summary_id, revision, source_order,
            source_kind, message_id, canonicalization_version, content_hash)
            VALUES ($1,$2,$3,'message',$4,$5,$6)`,
			result.SummaryID,
			result.Revision,
			i,
			doc.SourceID,
			doc.CanonicalizationVersion,
			doc.ContentHash,
		)
		if err != nil {
			return err
		}
	}
	for i, ref := range result.SummarySources {
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO summary_sources (summary_id, revision, source_order,
            source_kind, child_summary_id, child_revision) VALUES ($1,$2,$3,'summary',$4,$5)`,
			result.SummaryID,
			result.Revision,
			len(frozen)+i,
			ref.SummaryID,
			ref.Revision,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) GetSummary(
	ctx context.Context, owner, summaryID string, revision int64,
) (domain.SummaryVersion, error) {
	if owner == "" {
		return domain.SummaryVersion{}, ErrUnauthorized
	}
	if summaryID == "" || revision < 0 {
		return domain.SummaryVersion{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := getSummaryTx(ctx, tx, owner, summaryID, revision)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	return result, tx.Commit()
}

func getSummaryTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, summaryID string,
	revision int64,
) (domain.SummaryVersion, error) {
	result, err := scanSummary(tx.QueryRowContext(ctx, summarySelectSQL+`
        WHERE summary_id = $1 AND owner_user_id = $2 AND ($3::bigint = 0 OR revision = $3)
        ORDER BY revision DESC LIMIT 1`, summaryID, owner, revision))
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	if err = loadSummarySources(ctx, tx, &result); err != nil {
		return domain.SummaryVersion{}, err
	}
	if err = validateSummaryAccess(ctx, tx, owner, result.SummaryID, result.Revision); err != nil {
		return domain.SummaryVersion{}, err
	}
	return result, nil
}

func scanSummary(row interface{ Scan(...any) error }) (domain.SummaryVersion, error) {
	var result domain.SummaryVersion
	var claims []byte
	err := row.Scan(&result.SummaryID, &result.Revision, &result.OwnerUserID, &result.SubjectType,
		&result.SubjectID, &result.Title, &result.Body, &claims, &result.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SummaryVersion{}, ErrNotFound
	}
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	if err = json.Unmarshal(claims, &result.Claims); err != nil {
		return domain.SummaryVersion{}, err
	}
	result.Coverage = []domain.MessageVersionRef{}
	return result, nil
}

func loadSummarySources(ctx context.Context, tx *sql.Tx, result *domain.SummaryVersion) error {
	rows, err := tx.QueryContext(ctx, `SELECT source_kind, COALESCE(message_id,''),
        COALESCE(canonicalization_version,''), COALESCE(content_hash,''), COALESCE(child_summary_id,''),
        COALESCE(child_revision,0) FROM summary_sources WHERE summary_id = $1 AND revision = $2
        ORDER BY source_order`, result.SummaryID, result.Revision)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var message domain.MessageVersionRef
		var child domain.SummaryRef
		if err = rows.Scan(&kind, &message.MessageID, &message.CanonicalizationVersion,
			&message.ContentHash, &child.SummaryID, &child.Revision); err != nil {
			return err
		}
		if kind == "message" {
			result.Coverage = append(result.Coverage, message)
		} else {
			result.SummarySources = append(result.SummarySources, child)
		}
	}
	return rows.Err()
}

func validateSummaryAccess(
	ctx context.Context,
	tx *sql.Tx,
	owner, summaryID string,
	revision int64,
) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE dependencies(summary_id, revision) AS (
        SELECT $1::text, $2::bigint UNION SELECT src.child_summary_id, src.child_revision
        FROM summary_sources src JOIN dependencies d USING (summary_id, revision)
        WHERE src.source_kind = 'summary')
        SELECT DISTINCT COALESCE(v.owner_user_id,''), COALESCE(src.message_id,'') FROM dependencies d
        LEFT JOIN summary_versions v USING (summary_id, revision)
        LEFT JOIN summary_sources src USING (summary_id, revision) LIMIT 4097`, summaryID, revision)
	if err != nil {
		return err
	}
	var ids []string
	rowCount := 0
	for rows.Next() {
		rowCount++
		if rowCount > 4096 {
			_ = rows.Close()
			return ErrConflict
		}
		var sourceOwner, id string
		if err = rows.Scan(&sourceOwner, &id); err != nil {
			_ = rows.Close()
			return err
		}
		if sourceOwner != owner {
			_ = rows.Close()
			return ErrNotFound
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		doc, readErr := loadSearchMessage(ctx, tx, id)
		if readErr != nil {
			return readErr
		}
		if doc.OwnerUserID != owner {
			return ErrNotFound
		}
	}
	return nil
}

func (s *PostgresStore) ReadMessageVersion(
	ctx context.Context, owner string, ref domain.MessageVersionRef,
) (domain.SearchDocument, error) {
	if owner == "" {
		return domain.SearchDocument{}, ErrUnauthorized
	}
	if !ref.Valid() {
		return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SearchDocument{}, err
	}
	defer func() { _ = tx.Rollback() }()
	doc, err := readMessageVersionTx(ctx, tx, owner, ref)
	if err != nil {
		return domain.SearchDocument{}, err
	}
	return doc, tx.Commit()
}

func readMessageVersionTx(ctx context.Context, tx *sql.Tx, owner string,
	ref domain.MessageVersionRef,
) (domain.SearchDocument, error) {
	current, err := loadSearchMessage(ctx, tx, ref.MessageID)
	if err != nil {
		return domain.SearchDocument{}, err
	}
	if current.OwnerUserID != owner {
		return domain.SearchDocument{}, ErrNotFound
	}
	if current.ContentHash == ref.ContentHash &&
		current.CanonicalizationVersion == ref.CanonicalizationVersion {
		return current, nil
	}
	var doc domain.SearchDocument
	doc.Kind, doc.SourceID, doc.ContentHash, doc.CanonicalizationVersion = "message", ref.MessageID, ref.ContentHash, ref.CanonicalizationVersion
	err = tx.QueryRowContext(ctx, `SELECT owner_user_id, agent_id, session_id, conversation_id, role,
        session_seq, message_created_at, body FROM message_snapshots WHERE message_id = $1
        AND canonicalization_version = $2 AND content_hash = $3`, ref.MessageID, ref.CanonicalizationVersion, ref.ContentHash).
		Scan(&doc.OwnerUserID, &doc.AgentID, &doc.SessionID, &doc.ConversationID, &doc.Role,
			&doc.SessionSeq, &doc.CreatedAt, &doc.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
	}
	if err != nil {
		return domain.SearchDocument{}, err
	}
	if doc.OwnerUserID != owner {
		return domain.SearchDocument{}, ErrNotFound
	}
	if domain.SearchContentHash(doc.Body) != ref.ContentHash {
		return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
	}
	return doc, nil
}

func loadSummarySearchDocument(
	ctx context.Context,
	tx *sql.Tx,
	sourceID string,
) (domain.SearchDocument, error) {
	result, err := scanSummary(tx.QueryRowContext(ctx, summarySelectSQL+`
        WHERE summary_id = $1 ORDER BY revision DESC LIMIT 1`, sourceID))
	if err != nil {
		return domain.SearchDocument{}, err
	}
	if err = validateSummaryAccess(ctx, tx, result.OwnerUserID, result.SummaryID, result.Revision); err != nil {
		return domain.SearchDocument{}, err
	}
	return domain.SearchDocument{
		Kind:                    "summary",
		SourceID:                result.SummaryID,
		OwnerUserID:             result.OwnerUserID,
		Title:                   result.Title,
		Body:                    result.Body,
		ContentHash:             domain.SearchContentHash(result.Body),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		SummaryRevision:         result.Revision,
		CreatedAt:               result.CreatedAt,
	}, nil
}

func summaryWithoutReceipts(request domain.PublishSummaryRequest) domain.PublishSummaryRequest {
	request.Coverage = append([]domain.MessageVersionRef(nil), request.Coverage...)
	for i := range request.Coverage {
		request.Coverage[i].ReadReceipt = ""
	}
	request.Claims = append([]domain.SummaryClaim(nil), request.Claims...)
	for i := range request.Claims {
		request.Claims[i].Evidence = append(
			[]domain.SummaryEvidence(nil),
			request.Claims[i].Evidence...)
		for j := range request.Claims[i].Evidence {
			request.Claims[i].Evidence[j].ReadReceipt = ""
			request.Claims[i].Evidence[j].Message.ReadReceipt = ""
		}
	}
	return request
}

func validateSummaryQuotes(
	request domain.PublishSummaryRequest,
	docs []domain.SearchDocument,
) error {
	text := make(map[string]string, len(docs))
	for _, doc := range docs {
		ref := domain.MessageVersionRef{MessageID: doc.SourceID, ContentHash: doc.ContentHash,
			CanonicalizationVersion: doc.CanonicalizationVersion}
		text[ref.Identity()] = doc.Body
	}
	for _, claim := range request.Claims {
		for _, evidence := range claim.Evidence {
			if !strings.Contains(text[evidence.Message.Identity()], evidence.Quote) {
				return fmt.Errorf(
					"claim %s quote does not match coverage: %w",
					claim.ID,
					ErrConflict,
				)
			}
		}
	}
	return nil
}

func summaryRetryable(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && (pgError.Code == "40001" || pgError.Code == "23505")
}

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) CreateArtifactUpload(
	ctx context.Context,
	principal UserPrincipal,
	req CreateArtifactUploadRequest,
	bucket string,
	object string,
	expiresAt time.Time,
) (ArtifactUpload, error) {
	uploadID, err := newSecret("artup")
	if err != nil {
		return ArtifactUpload{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(
		ctx,
		`
		INSERT INTO artifact_uploads (
			upload_id, owner_user_id, session_id, kind, title, summary, filename, content_type,
			size_bytes, sha256, bucket, object, status, expires_at, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)
		RETURNING upload_id, artifact_id, owner_user_id, node_id, agent_id, session_id, kind, title, summary, filename,
			content_type, size_bytes, sha256, bucket, object, generation, status, expires_at,
			completed_at, created_at, updated_at
	`,
		uploadID,
		principal.User.UserID,
		strings.TrimSpace(req.SessionID),
		strings.TrimSpace(req.Kind),
		strings.TrimSpace(
			req.Title,
		),
		strings.TrimSpace(req.Summary),
		strings.TrimSpace(req.Filename),
		strings.TrimSpace(
			req.ContentType,
		),
		req.SizeBytes,
		strings.TrimSpace(req.SHA256),
		bucket,
		object,
		domain.ArtifactUploadStatusPending,
		expiresAt,
		now,
	)
	return scanArtifactUpload(row)
}

func (s *PostgresStore) GetArtifactUpload(
	ctx context.Context,
	principal UserPrincipal,
	uploadID string,
) (ArtifactUpload, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT upload_id, artifact_id, owner_user_id, node_id, agent_id, session_id, kind, title, summary, filename, content_type,
			size_bytes, sha256, bucket, object, generation, status, expires_at, completed_at,
			created_at, updated_at
		FROM artifact_uploads
		WHERE upload_id = $1 AND owner_user_id = $2
	`, uploadID, principal.User.UserID)
	return scanArtifactUpload(row)
}

func (s *PostgresStore) CompleteArtifactUpload(
	ctx context.Context,
	principal UserPrincipal,
	uploadID string,
	attrs ArtifactContent,
	req CompleteArtifactUploadRequest,
) (ArtifactUpload, SessionArtifact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	defer func() { _ = tx.Rollback() }()

	upload, err := scanArtifactUpload(tx.QueryRowContext(ctx, `
		SELECT upload_id, artifact_id, owner_user_id, node_id, agent_id, session_id, kind, title, summary, filename, content_type,
			size_bytes, sha256, bucket, object, generation, status, expires_at, completed_at,
			created_at, updated_at
		FROM artifact_uploads
		WHERE upload_id = $1 AND owner_user_id = $2
		FOR UPDATE
	`, uploadID, principal.User.UserID))
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	if upload.Status == domain.ArtifactUploadStatusCompleted ||
		s.now().UTC().After(upload.ExpiresAt) {
		return upload, SessionArtifact{}, ErrConflict
	}

	content := ArtifactContent{
		Ref:         "main",
		Filename:    firstNonEmpty(attrs.Filename, upload.Filename),
		ContentType: firstNonEmpty(attrs.ContentType, upload.ContentType),
		SizeBytes:   firstNonZeroInt64(attrs.SizeBytes, upload.SizeBytes),
		SHA256:      firstNonEmpty(attrs.SHA256, upload.SHA256),
		Bucket:      upload.Bucket,
		Object:      upload.Object,
		Generation:  attrs.Generation,
		StorageURI:  "gs://" + upload.Bucket + "/" + upload.Object,
	}
	artifactReq := CreateSessionArtifactRequest{
		Kind:          firstNonEmpty(req.Kind, upload.Kind),
		SchemaVersion: req.SchemaVersion,
		Title:         firstNonEmpty(req.Title, upload.Title, upload.Filename),
		Summary:       firstNonEmpty(req.Summary, upload.Summary),
		Status:        firstNonEmpty(req.Status, domain.SessionArtifactStatusAvailable),
		SessionID:     firstNonEmpty(req.SessionID, upload.SessionID),
		MessageID:     req.MessageID,
		NodeID:        req.NodeID,
		AgentID:       req.AgentID,
		SourceJSON:    req.SourceJSON,
		PayloadJSON:   req.PayloadJSON,
		Contents:      []ArtifactContent{content},
	}
	artifact, err := s.createSessionArtifactTx(ctx, tx, principal, artifactReq)
	if err != nil {
		return upload, SessionArtifact{}, err
	}

	now := s.now().UTC()
	updated, err := scanArtifactUpload(tx.QueryRowContext(ctx, `
		UPDATE artifact_uploads
		SET status = $1, generation = $2, completed_at = $3, updated_at = $3
		WHERE upload_id = $4
		RETURNING upload_id, artifact_id, owner_user_id, node_id, agent_id, session_id, kind, title, summary, filename,
			content_type, size_bytes, sha256, bucket, object, generation, status, expires_at,
			completed_at, created_at, updated_at
	`, domain.ArtifactUploadStatusCompleted, content.Generation, now, upload.UploadID))
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	return updated, artifact, nil
}

func (s *PostgresStore) CreateSessionArtifact(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSessionArtifactRequest,
) (SessionArtifact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionArtifact{}, err
	}
	defer func() { _ = tx.Rollback() }()
	artifact, err := s.createSessionArtifactTx(ctx, tx, principal, req)
	if err != nil {
		return SessionArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionArtifact{}, err
	}
	return artifact, nil
}

func (s *PostgresStore) createSessionArtifactTx(
	ctx context.Context,
	tx *sql.Tx,
	principal UserPrincipal,
	req CreateSessionArtifactRequest,
) (SessionArtifact, error) {
	artifactID := strings.TrimSpace(req.ArtifactID)
	if artifactID == "" {
		generated, err := newSecret("art")
		if err != nil {
			return SessionArtifact{}, err
		}
		artifactID = generated
	}
	now := s.now().UTC()
	schemaVersion := req.SchemaVersion
	if schemaVersion <= 0 {
		schemaVersion = 1
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = domain.SessionArtifactStatusAvailable
	}
	artifact, err := scanSessionArtifact(tx.QueryRowContext(
		ctx,
		`
		INSERT INTO session_artifacts (
			artifact_id, owner_user_id, kind, schema_version, title, summary, status,
			session_id, message_id, node_id, agent_id, source_json, payload_json,
			created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,COALESCE($12, '{}'::jsonb),
			COALESCE($13, '{}'::jsonb),$14,$14)
		RETURNING artifact_id, owner_user_id, kind, schema_version, title, summary, status,
			session_id, message_id, node_id, agent_id, source_json, payload_json,
			created_at, updated_at, deleted_at
	`,
		artifactID,
		principal.User.UserID,
		strings.TrimSpace(req.Kind),
		schemaVersion,
		strings.TrimSpace(req.Title),
		strings.TrimSpace(req.Summary),
		status,
		strings.TrimSpace(
			req.SessionID,
		),
		strings.TrimSpace(req.MessageID),
		strings.TrimSpace(req.NodeID),
		strings.TrimSpace(req.AgentID),
		nullRaw(req.SourceJSON),
		nullRaw(req.PayloadJSON),
		now,
	))
	if err != nil {
		return SessionArtifact{}, err
	}
	for _, content := range req.Contents {
		if content.Ref == "" {
			content.Ref = "main"
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO session_artifact_contents (
				artifact_id, ref, filename, content_type, size_bytes, sha256, bucket, object,
				generation, storage_uri, text_content, created_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		`, artifact.ArtifactID, content.Ref, content.Filename, content.ContentType, content.SizeBytes,
			content.SHA256, content.Bucket, content.Object, content.Generation, content.StorageURI,
			content.Text, now)
		if err != nil {
			return SessionArtifact{}, err
		}
	}
	contents, err := s.listArtifactContentsTx(ctx, tx, artifact.ArtifactID)
	if err != nil {
		return SessionArtifact{}, err
	}
	artifact.Contents = contents
	return artifact, nil
}

func (s *PostgresStore) GetSessionArtifact(
	ctx context.Context,
	principal UserPrincipal,
	artifactID string,
) (SessionArtifact, error) {
	artifact, err := scanSessionArtifact(s.db.QueryRowContext(ctx, `
		SELECT artifact_id, owner_user_id, kind, schema_version, title, summary, status,
			session_id, message_id, node_id, agent_id, source_json, payload_json,
			created_at, updated_at, deleted_at
		FROM session_artifacts
		WHERE artifact_id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
	`, artifactID, principal.User.UserID))
	if err != nil {
		return SessionArtifact{}, err
	}
	contents, err := s.listArtifactContents(ctx, artifact.ArtifactID)
	if err != nil {
		return SessionArtifact{}, err
	}
	artifact.Contents = contents
	return artifact, nil
}

func (s *PostgresStore) ListSessionArtifacts(
	ctx context.Context,
	filter ListSessionArtifactsFilter,
) ([]SessionArtifact, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_id, owner_user_id, kind, schema_version, title, summary, status,
			session_id, message_id, node_id, agent_id, source_json, payload_json,
			created_at, updated_at, deleted_at
		FROM session_artifacts
		WHERE owner_user_id = $1
			AND deleted_at IS NULL
			AND ($2 = '' OR session_id = $2)
			AND ($3 = '' OR kind = $3)
			AND ($4 = '' OR status = $4)
		ORDER BY created_at DESC, artifact_id DESC
		LIMIT $5
	`, filter.Principal.User.UserID, filter.SessionID, filter.Kind, filter.Status, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]SessionArtifact, 0)
	for rows.Next() {
		artifact, err := scanSessionArtifact(rows)
		if err != nil {
			return nil, err
		}
		contents, err := s.listArtifactContents(ctx, artifact.ArtifactID)
		if err != nil {
			return nil, err
		}
		artifact.Contents = contents
		out = append(out, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetArtifactContent(
	ctx context.Context,
	principal UserPrincipal,
	artifactID string,
	ref string,
) (SessionArtifact, ArtifactContent, error) {
	if ref == "" {
		ref = "main"
	}
	artifact, err := s.GetSessionArtifact(ctx, principal, artifactID)
	if err != nil {
		return SessionArtifact{}, ArtifactContent{}, err
	}
	content, err := scanArtifactContent(s.db.QueryRowContext(ctx, `
		SELECT artifact_id, ref, filename, content_type, size_bytes, sha256, bucket, object,
			generation, storage_uri, text_content, created_at
		FROM session_artifact_contents
		WHERE artifact_id = $1 AND ref = $2
	`, artifactID, ref))
	if err != nil {
		return SessionArtifact{}, ArtifactContent{}, err
	}
	return artifact, content, nil
}

func (s *PostgresStore) AttachSessionArtifact(
	ctx context.Context,
	principal UserPrincipal,
	req AttachArtifactRequest,
) (SessionArtifact, error) {
	now := s.now().UTC()
	artifact, err := scanSessionArtifact(s.db.QueryRowContext(ctx, `
		UPDATE session_artifacts
		SET message_id = $1,
			session_id = CASE WHEN $2 = '' THEN session_id ELSE $2 END,
			updated_at = $3
		WHERE artifact_id = $4 AND owner_user_id = $5 AND deleted_at IS NULL
		RETURNING artifact_id, owner_user_id, kind, schema_version, title, summary, status,
			session_id, message_id, node_id, agent_id, source_json, payload_json,
			created_at, updated_at, deleted_at
	`, strings.TrimSpace(req.MessageID), strings.TrimSpace(req.SessionID), now, req.ArtifactID,
		principal.User.UserID))
	if err != nil {
		return SessionArtifact{}, err
	}
	contents, err := s.listArtifactContents(ctx, artifact.ArtifactID)
	if err != nil {
		return SessionArtifact{}, err
	}
	artifact.Contents = contents
	return artifact, nil
}

func (s *PostgresStore) listArtifactContents(
	ctx context.Context,
	artifactID string,
) ([]ArtifactContent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact_id, ref, filename, content_type, size_bytes, sha256, bucket, object,
			generation, storage_uri, text_content, created_at
		FROM session_artifact_contents
		WHERE artifact_id = $1
		ORDER BY ref ASC
	`, artifactID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanArtifactContentRows(rows)
}

func (s *PostgresStore) listArtifactContentsTx(
	ctx context.Context,
	tx *sql.Tx,
	artifactID string,
) ([]ArtifactContent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT artifact_id, ref, filename, content_type, size_bytes, sha256, bucket, object,
			generation, storage_uri, text_content, created_at
		FROM session_artifact_contents
		WHERE artifact_id = $1
		ORDER BY ref ASC
	`, artifactID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanArtifactContentRows(rows)
}

func scanArtifactUpload(row interface {
	Scan(dest ...any) error
}) (ArtifactUpload, error) {
	var upload ArtifactUpload
	if err := row.Scan(
		&upload.UploadID,
		&upload.ArtifactID,
		&upload.OwnerUserID,
		&upload.NodeID,
		&upload.AgentID,
		&upload.SessionID,
		&upload.Kind,
		&upload.Title,
		&upload.Summary,
		&upload.Filename,
		&upload.ContentType,
		&upload.SizeBytes,
		&upload.SHA256,
		&upload.Bucket,
		&upload.Object,
		&upload.Generation,
		&upload.Status,
		&upload.ExpiresAt,
		&upload.CompletedAt,
		&upload.CreatedAt,
		&upload.UpdatedAt,
	); err != nil {
		return ArtifactUpload{}, mapSQLError(err)
	}
	return upload, nil
}

func scanSessionArtifact(row interface {
	Scan(dest ...any) error
}) (SessionArtifact, error) {
	var artifact SessionArtifact
	var sourceJSON []byte
	var payloadJSON []byte
	if err := row.Scan(
		&artifact.ArtifactID,
		&artifact.OwnerUserID,
		&artifact.Kind,
		&artifact.SchemaVersion,
		&artifact.Title,
		&artifact.Summary,
		&artifact.Status,
		&artifact.SessionID,
		&artifact.MessageID,
		&artifact.NodeID,
		&artifact.AgentID,
		&sourceJSON,
		&payloadJSON,
		&artifact.CreatedAt,
		&artifact.UpdatedAt,
		&artifact.DeletedAt,
	); err != nil {
		return SessionArtifact{}, mapSQLError(err)
	}
	artifact.SourceJSON = append(json.RawMessage(nil), sourceJSON...)
	artifact.PayloadJSON = append(json.RawMessage(nil), payloadJSON...)
	return artifact, nil
}

func scanArtifactContent(row interface {
	Scan(dest ...any) error
}) (ArtifactContent, error) {
	var content ArtifactContent
	if err := row.Scan(
		&content.ArtifactID,
		&content.Ref,
		&content.Filename,
		&content.ContentType,
		&content.SizeBytes,
		&content.SHA256,
		&content.Bucket,
		&content.Object,
		&content.Generation,
		&content.StorageURI,
		&content.Text,
		&content.CreatedAt,
	); err != nil {
		return ArtifactContent{}, mapSQLError(err)
	}
	return content, nil
}

func scanArtifactContentRows(rows *sql.Rows) ([]ArtifactContent, error) {
	out := make([]ArtifactContent, 0)
	for rows.Next() {
		content, err := scanArtifactContent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, content)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

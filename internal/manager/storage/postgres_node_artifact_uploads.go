package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const artifactUploadSelectColumns = `
	upload_id, artifact_id, owner_user_id, node_id, agent_id, session_id, kind, title,
	summary, filename, content_type, size_bytes, sha256, bucket, object, generation,
	status, expires_at, completed_at, created_at, updated_at
`

func (s *PostgresStore) PrepareArtifactPublication(
	ctx context.Context,
	node Node,
	publicationID string,
	req PrepareArtifactPublicationRequest,
	bucket string,
	object string,
	expiresAt time.Time,
) (ArtifactPublication, ArtifactUpload, error) {
	uploadID, err := newSecret("artup")
	if err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	artifactID, err := newSecret("art")
	if err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	defer func() { _ = tx.Rollback() }()

	publication, err := scanArtifactPublication(tx.QueryRowContext(ctx, `
		SELECT `+artifactPublicationSelectColumns+`
		FROM artifact_publications
		WHERE publication_id = $1 AND node_id = $2 AND owner_user_id = $3
		FOR UPDATE
	`, publicationID, node.NodeID, node.OwnerUserID))
	if err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	now := s.now().UTC()
	upload, err := scanArtifactUpload(tx.QueryRowContext(ctx, `
		INSERT INTO artifact_uploads (
			upload_id, artifact_id, owner_user_id, node_id, agent_id, session_id,
			kind, title, filename, content_type, size_bytes, sha256, bucket, object,
			status, expires_at, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,'file',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
		ON CONFLICT (owner_user_id, session_id, filename, sha256)
			WHERE node_id <> '' AND sha256 <> ''
		DO UPDATE SET
			expires_at = CASE
				WHEN artifact_uploads.status = 'completed'
					THEN artifact_uploads.expires_at
				ELSE EXCLUDED.expires_at
			END,
			updated_at = CASE
				WHEN artifact_uploads.status = 'completed'
					THEN artifact_uploads.updated_at
				ELSE EXCLUDED.updated_at
			END
		RETURNING `+artifactUploadSelectColumns+`
	`, uploadID, artifactID, publication.OwnerUserID, publication.NodeID,
		publication.AgentID, publication.SessionID, publication.Title, req.Filename,
		req.ContentType, req.SizeBytes, req.SHA256, bucket, object,
		domain.ArtifactUploadStatusPending, expiresAt, now))
	if err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	if upload.SizeBytes != req.SizeBytes {
		return ArtifactPublication{}, ArtifactUpload{}, ErrConflict
	}
	status := domain.ArtifactPublicationStatusUploading
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		status = domain.ArtifactPublicationStatusAvailable
	}
	publication, err = scanArtifactPublication(tx.QueryRowContext(ctx, `
		UPDATE artifact_publications
		SET artifact_id = $1, status = $2, updated_at = $3
		WHERE publication_id = $4
		RETURNING `+artifactPublicationSelectColumns+`
	`, upload.ArtifactID, status, now, publication.PublicationID))
	if err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactPublication{}, ArtifactUpload{}, err
	}
	return publication, upload, nil
}

func (s *PostgresStore) GetNodeArtifactUpload(
	ctx context.Context,
	node Node,
	uploadID string,
) (ArtifactUpload, error) {
	return scanArtifactUpload(s.db.QueryRowContext(ctx, `
		SELECT `+artifactUploadSelectColumns+`
		FROM artifact_uploads
		WHERE upload_id = $1 AND node_id = $2 AND owner_user_id = $3
	`, uploadID, node.NodeID, node.OwnerUserID))
}

func (s *PostgresStore) CompleteNodeArtifactUpload(
	ctx context.Context,
	node Node,
	uploadID string,
	attrs ArtifactContent,
) (ArtifactUpload, SessionArtifact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	defer func() { _ = tx.Rollback() }()

	upload, err := scanArtifactUpload(tx.QueryRowContext(ctx, `
		SELECT `+artifactUploadSelectColumns+`
		FROM artifact_uploads
		WHERE upload_id = $1 AND node_id = $2 AND owner_user_id = $3
		FOR UPDATE
	`, uploadID, node.NodeID, node.OwnerUserID))
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		artifact, err := s.getSessionArtifactTx(ctx, tx, upload.ArtifactID)
		if err != nil {
			return ArtifactUpload{}, SessionArtifact{}, err
		}
		if err := tx.Commit(); err != nil {
			return ArtifactUpload{}, SessionArtifact{}, err
		}
		return upload, artifact, nil
	}
	content := ArtifactContent{
		Ref:         "main",
		Filename:    upload.Filename,
		ContentType: firstNonEmpty(attrs.ContentType, upload.ContentType),
		SizeBytes:   attrs.SizeBytes,
		SHA256:      upload.SHA256,
		Bucket:      upload.Bucket,
		Object:      upload.Object,
		Generation:  attrs.Generation,
		StorageURI:  "s3://" + upload.Bucket + "/" + upload.Object,
	}
	principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
	artifact, err := s.createSessionArtifactTx(ctx, tx, principal, CreateSessionArtifactRequest{
		ArtifactID:    upload.ArtifactID,
		Kind:          "file",
		SchemaVersion: 1,
		Title:         firstNonEmpty(upload.Title, upload.Filename),
		Status:        domain.SessionArtifactStatusAvailable,
		SessionID:     upload.SessionID,
		NodeID:        upload.NodeID,
		AgentID:       upload.AgentID,
		Contents:      []ArtifactContent{content},
	})
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	now := s.now().UTC()
	upload, err = scanArtifactUpload(tx.QueryRowContext(ctx, `
		UPDATE artifact_uploads
		SET status = $1, generation = $2, completed_at = $3, updated_at = $3
		WHERE upload_id = $4
		RETURNING `+artifactUploadSelectColumns+`
	`, domain.ArtifactUploadStatusCompleted, attrs.Generation, now, upload.UploadID))
	if err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE artifact_publications
		SET status = $1, updated_at = $2
		WHERE artifact_id = $3
	`, domain.ArtifactPublicationStatusAvailable, now, upload.ArtifactID); err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactUpload{}, SessionArtifact{}, err
	}
	return upload, artifact, nil
}

func (s *PostgresStore) getSessionArtifactTx(
	ctx context.Context,
	tx *sql.Tx,
	artifactID string,
) (SessionArtifact, error) {
	artifact, err := scanSessionArtifact(tx.QueryRowContext(ctx, `
		SELECT artifact_id, owner_user_id, kind, schema_version, title, summary, status,
			session_id, message_id, node_id, agent_id, source_json, payload_json,
			created_at, updated_at, deleted_at
		FROM session_artifacts
		WHERE artifact_id = $1 AND deleted_at IS NULL
	`, artifactID))
	if err != nil {
		return SessionArtifact{}, err
	}
	contents, err := s.listArtifactContentsTx(ctx, tx, artifactID)
	if err != nil {
		return SessionArtifact{}, err
	}
	artifact.Contents = contents
	return artifact, nil
}

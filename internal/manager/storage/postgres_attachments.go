package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const userAttachmentColumns = `attachment_id, owner_user_id, COALESCE(conversation_id, ''),
	filename, content_type, size_bytes, sha256, bucket, object, generation, upload_status,
	upload_expires_at, completed_at, created_at, updated_at`

func (s *PostgresStore) CreateUserAttachment(
	ctx context.Context,
	principal UserPrincipal,
	req CreateUserAttachmentRequest,
	bucket string,
	object string,
	expiresAt time.Time,
) (UserAttachment, error) {
	attachmentID, err := newSecret("att")
	if err != nil {
		return UserAttachment{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO user_attachments (
			attachment_id, owner_user_id, conversation_id, filename, content_type,
			size_bytes, sha256, bucket, object, upload_status, upload_expires_at,
			created_at, updated_at
		)
		VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		RETURNING `+userAttachmentColumns,
		attachmentID, principal.User.UserID, strings.TrimSpace(req.ConversationID),
		strings.TrimSpace(req.Filename), strings.TrimSpace(req.ContentType), req.SizeBytes,
		strings.TrimSpace(req.SHA256), bucket, object, domain.UserAttachmentUploadPending,
		expiresAt, now)
	return scanUserAttachment(row)
}

func (s *PostgresStore) GetUserAttachment(
	ctx context.Context,
	principal UserPrincipal,
	attachmentID string,
) (UserAttachment, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+userAttachmentColumns+`
		FROM user_attachments
		WHERE attachment_id = $1 AND owner_user_id = $2
	`, attachmentID, principal.User.UserID)
	return scanUserAttachment(row)
}

func (s *PostgresStore) CompleteUserAttachment(
	ctx context.Context,
	principal UserPrincipal,
	attachmentID string,
	attrs ArtifactContent,
) (UserAttachment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UserAttachment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attachment, err := scanUserAttachment(tx.QueryRowContext(ctx, `
		SELECT `+userAttachmentColumns+`
		FROM user_attachments
		WHERE attachment_id = $1 AND owner_user_id = $2
		FOR UPDATE
	`, attachmentID, principal.User.UserID))
	if err != nil {
		return UserAttachment{}, err
	}
	if attachment.UploadStatus == domain.UserAttachmentUploadCompleted {
		return attachment, tx.Commit()
	}
	now := s.now().UTC()
	updated, err := scanUserAttachment(tx.QueryRowContext(ctx, `
		UPDATE user_attachments
		SET generation = $1, size_bytes = $2, content_type = $3,
			upload_status = $4, completed_at = $5, updated_at = $5
		WHERE attachment_id = $6
		RETURNING `+userAttachmentColumns,
		attrs.Generation, attrs.SizeBytes, firstNonEmpty(attrs.ContentType, attachment.ContentType),
		domain.UserAttachmentUploadCompleted, now, attachmentID))
	if err != nil {
		return UserAttachment{}, err
	}
	if err := tx.Commit(); err != nil {
		return UserAttachment{}, err
	}
	return updated, nil
}

type userAttachmentScanner interface {
	Scan(dest ...any) error
}

func scanUserAttachment(row userAttachmentScanner) (UserAttachment, error) {
	var attachment UserAttachment
	err := row.Scan(
		&attachment.AttachmentID, &attachment.OwnerUserID, &attachment.ConversationID,
		&attachment.Filename, &attachment.ContentType, &attachment.SizeBytes, &attachment.SHA256,
		&attachment.Bucket, &attachment.Object, &attachment.Generation, &attachment.UploadStatus,
		&attachment.UploadExpiresAt, &attachment.CompletedAt, &attachment.CreatedAt,
		&attachment.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return UserAttachment{}, ErrNotFound
	}
	return attachment, err
}

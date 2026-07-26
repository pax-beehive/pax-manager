package storage

import (
	"context"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateUserAttachment(
	ctx context.Context,
	principal UserPrincipal,
	req CreateUserAttachmentRequest,
	bucket string,
	object string,
	expiresAt time.Time,
) (UserAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attachmentID, err := newSecret("att")
	if err != nil {
		return UserAttachment{}, err
	}
	now := s.now().UTC()
	attachment := UserAttachment{
		AttachmentID:    attachmentID,
		OwnerUserID:     principal.User.UserID,
		ConversationID:  strings.TrimSpace(req.ConversationID),
		Filename:        strings.TrimSpace(req.Filename),
		ContentType:     strings.TrimSpace(req.ContentType),
		SizeBytes:       req.SizeBytes,
		SHA256:          strings.TrimSpace(req.SHA256),
		Bucket:          bucket,
		Object:          object,
		UploadStatus:    domain.UserAttachmentUploadPending,
		UploadExpiresAt: expiresAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.userAttachments[attachmentID] = attachment
	return attachment, nil
}

func (s *MemoryStore) GetUserAttachment(
	ctx context.Context,
	principal UserPrincipal,
	attachmentID string,
) (UserAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attachment, ok := s.userAttachments[attachmentID]
	if !ok || !canAccessOwner(principal, attachment.OwnerUserID) {
		return UserAttachment{}, ErrNotFound
	}
	return attachment, nil
}

func (s *MemoryStore) CompleteUserAttachment(
	ctx context.Context,
	principal UserPrincipal,
	attachmentID string,
	attrs ArtifactContent,
) (UserAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attachment, ok := s.userAttachments[attachmentID]
	if !ok || !canAccessOwner(principal, attachment.OwnerUserID) {
		return UserAttachment{}, ErrNotFound
	}
	if attachment.UploadStatus == domain.UserAttachmentUploadCompleted {
		return attachment, nil
	}
	now := s.now().UTC()
	attachment.Generation = attrs.Generation
	attachment.SizeBytes = attrs.SizeBytes
	attachment.ContentType = firstNonEmpty(attrs.ContentType, attachment.ContentType)
	attachment.UploadStatus = domain.UserAttachmentUploadCompleted
	attachment.CompletedAt = &now
	attachment.UpdatedAt = now
	s.userAttachments[attachmentID] = attachment
	return attachment, nil
}

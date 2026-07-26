package storage

import (
	"context"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) PrepareArtifactPublication(
	ctx context.Context,
	node Node,
	publicationID string,
	req PrepareArtifactPublicationRequest,
	bucket string,
	object string,
	expiresAt time.Time,
) (ArtifactPublication, ArtifactUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	publication, ok := s.artifactPublications[publicationID]
	if !ok ||
		publication.NodeID != node.NodeID ||
		publication.OwnerUserID != node.OwnerUserID {
		return ArtifactPublication{}, ArtifactUpload{}, ErrNotFound
	}
	var upload ArtifactUpload
	for _, candidate := range s.artifactUploads {
		if candidate.NodeID != "" &&
			candidate.OwnerUserID == publication.OwnerUserID &&
			candidate.SessionID == publication.SessionID &&
			candidate.Filename == req.Filename &&
			candidate.SHA256 == req.SHA256 {
			upload = candidate
			break
		}
	}
	now := s.now().UTC()
	if upload.UploadID == "" {
		uploadID, err := newSecret("artup")
		if err != nil {
			return ArtifactPublication{}, ArtifactUpload{}, err
		}
		artifactID, err := newSecret("art")
		if err != nil {
			return ArtifactPublication{}, ArtifactUpload{}, err
		}
		upload = ArtifactUpload{
			UploadID:    uploadID,
			ArtifactID:  artifactID,
			OwnerUserID: publication.OwnerUserID,
			NodeID:      publication.NodeID,
			AgentID:     publication.AgentID,
			SessionID:   publication.SessionID,
			Kind:        "file",
			Title:       publication.Title,
			Filename:    req.Filename,
			ContentType: req.ContentType,
			SizeBytes:   req.SizeBytes,
			SHA256:      req.SHA256,
			Bucket:      bucket,
			Object:      object,
			Status:      domain.ArtifactUploadStatusPending,
			ExpiresAt:   expiresAt,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	} else if upload.SizeBytes != req.SizeBytes {
		return ArtifactPublication{}, ArtifactUpload{}, ErrConflict
	} else if upload.Status != domain.ArtifactUploadStatusCompleted {
		upload.ExpiresAt = expiresAt
		upload.UpdatedAt = now
	}
	s.artifactUploads[upload.UploadID] = upload
	publication.ArtifactID = upload.ArtifactID
	publication.UpdatedAt = now
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		publication.Status = domain.ArtifactPublicationStatusAvailable
	} else {
		publication.Status = domain.ArtifactPublicationStatusUploading
	}
	s.artifactPublications[publication.PublicationID] = publication
	return publication, upload, nil
}

func (s *MemoryStore) GetNodeArtifactUpload(
	ctx context.Context,
	node Node,
	uploadID string,
) (ArtifactUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, ok := s.artifactUploads[uploadID]
	if !ok || upload.NodeID != node.NodeID || upload.OwnerUserID != node.OwnerUserID {
		return ArtifactUpload{}, ErrNotFound
	}
	return upload, nil
}

func (s *MemoryStore) CompleteNodeArtifactUpload(
	ctx context.Context,
	node Node,
	uploadID string,
	attrs ArtifactContent,
) (ArtifactUpload, SessionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, ok := s.artifactUploads[uploadID]
	if !ok || upload.NodeID != node.NodeID || upload.OwnerUserID != node.OwnerUserID {
		return ArtifactUpload{}, SessionArtifact{}, ErrNotFound
	}
	principal := UserPrincipal{User: User{UserID: node.OwnerUserID}}
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		artifact, ok := s.sessionArtifacts[upload.ArtifactID]
		if !ok {
			return ArtifactUpload{}, SessionArtifact{}, ErrNotFound
		}
		return upload, s.artifactWithContentsLocked(artifact), nil
	}
	content := ArtifactContent{
		Ref:         "main",
		Filename:    upload.Filename,
		ContentType: firstNonEmpty(strings.TrimSpace(attrs.ContentType), upload.ContentType),
		SizeBytes:   attrs.SizeBytes,
		SHA256:      upload.SHA256,
		Bucket:      upload.Bucket,
		Object:      upload.Object,
		Generation:  attrs.Generation,
		StorageURI:  "gs://" + upload.Bucket + "/" + upload.Object,
	}
	artifact, err := s.createSessionArtifactLocked(principal, CreateSessionArtifactRequest{
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
	upload.Status = domain.ArtifactUploadStatusCompleted
	upload.Generation = attrs.Generation
	upload.CompletedAt = &now
	upload.UpdatedAt = now
	s.artifactUploads[upload.UploadID] = upload
	for id, publication := range s.artifactPublications {
		if publication.ArtifactID != upload.ArtifactID {
			continue
		}
		publication.Status = domain.ArtifactPublicationStatusAvailable
		publication.UpdatedAt = now
		s.artifactPublications[id] = publication
	}
	return upload, artifact, nil
}

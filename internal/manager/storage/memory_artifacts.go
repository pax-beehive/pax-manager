package storage

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) CreateArtifactUpload(
	ctx context.Context,
	principal UserPrincipal,
	req CreateArtifactUploadRequest,
	bucket string,
	object string,
	expiresAt time.Time,
) (ArtifactUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	uploadID, err := newSecret("artup")
	if err != nil {
		return ArtifactUpload{}, err
	}
	now := s.now().UTC()
	upload := ArtifactUpload{
		UploadID:    uploadID,
		OwnerUserID: principal.User.UserID,
		SessionID:   strings.TrimSpace(req.SessionID),
		Kind:        strings.TrimSpace(req.Kind),
		Title:       strings.TrimSpace(req.Title),
		Summary:     strings.TrimSpace(req.Summary),
		Filename:    strings.TrimSpace(req.Filename),
		ContentType: strings.TrimSpace(req.ContentType),
		SizeBytes:   req.SizeBytes,
		SHA256:      strings.TrimSpace(req.SHA256),
		Bucket:      bucket,
		Object:      object,
		Status:      domain.ArtifactUploadStatusPending,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.artifactUploads[uploadID] = upload
	return upload, nil
}

func (s *MemoryStore) GetArtifactUpload(
	ctx context.Context,
	principal UserPrincipal,
	uploadID string,
) (ArtifactUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, ok := s.artifactUploads[uploadID]
	if !ok || !canAccessOwner(principal, upload.OwnerUserID) {
		return ArtifactUpload{}, ErrNotFound
	}
	return upload, nil
}

func (s *MemoryStore) CompleteArtifactUpload(
	ctx context.Context,
	principal UserPrincipal,
	uploadID string,
	attrs ArtifactContent,
	req CompleteArtifactUploadRequest,
) (ArtifactUpload, SessionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, ok := s.artifactUploads[uploadID]
	if !ok || !canAccessOwner(principal, upload.OwnerUserID) {
		return ArtifactUpload{}, SessionArtifact{}, ErrNotFound
	}
	if upload.Status == domain.ArtifactUploadStatusCompleted {
		return upload, SessionArtifact{}, ErrConflict
	}
	if s.now().UTC().After(upload.ExpiresAt) {
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
		SourceJSON:    cloneRaw(req.SourceJSON),
		PayloadJSON:   cloneRaw(req.PayloadJSON),
		Contents:      []ArtifactContent{content},
	}
	artifact, err := s.createSessionArtifactLocked(principal, artifactReq)
	if err != nil {
		return upload, SessionArtifact{}, err
	}
	now := s.now().UTC()
	upload.Status = domain.ArtifactUploadStatusCompleted
	upload.Generation = content.Generation
	upload.CompletedAt = &now
	upload.UpdatedAt = now
	s.artifactUploads[upload.UploadID] = upload
	return upload, artifact, nil
}

func (s *MemoryStore) CreateSessionArtifact(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSessionArtifactRequest,
) (SessionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createSessionArtifactLocked(principal, req)
}

func (s *MemoryStore) createSessionArtifactLocked(
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
	artifact := SessionArtifact{
		ArtifactID:    artifactID,
		OwnerUserID:   principal.User.UserID,
		Kind:          strings.TrimSpace(req.Kind),
		SchemaVersion: req.SchemaVersion,
		Title:         strings.TrimSpace(req.Title),
		Summary:       strings.TrimSpace(req.Summary),
		Status:        strings.TrimSpace(req.Status),
		SessionID:     strings.TrimSpace(req.SessionID),
		MessageID:     strings.TrimSpace(req.MessageID),
		NodeID:        strings.TrimSpace(req.NodeID),
		AgentID:       strings.TrimSpace(req.AgentID),
		SourceJSON:    cloneRaw(req.SourceJSON),
		PayloadJSON:   cloneRaw(req.PayloadJSON),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if artifact.SchemaVersion <= 0 {
		artifact.SchemaVersion = 1
	}
	if artifact.Status == "" {
		artifact.Status = domain.SessionArtifactStatusAvailable
	}
	s.sessionArtifacts[artifactID] = artifact
	for _, content := range req.Contents {
		content.ArtifactID = artifactID
		if content.Ref == "" {
			content.Ref = "main"
		}
		if content.CreatedAt.IsZero() {
			content.CreatedAt = now
		}
		s.sessionArtifactContents[artifactContentKey{
			ArtifactID: artifactID,
			Ref:        content.Ref,
		}] = content
	}
	return s.artifactWithContentsLocked(artifact), nil
}

func (s *MemoryStore) GetSessionArtifact(
	ctx context.Context,
	principal UserPrincipal,
	artifactID string,
) (SessionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	artifact, ok := s.sessionArtifacts[artifactID]
	if !ok || artifact.DeletedAt != nil || !canAccessOwner(principal, artifact.OwnerUserID) {
		return SessionArtifact{}, ErrNotFound
	}
	return s.artifactWithContentsLocked(artifact), nil
}

func (s *MemoryStore) ListSessionArtifacts(
	ctx context.Context,
	filter ListSessionArtifactsFilter,
) ([]SessionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SessionArtifact, 0)
	for _, artifact := range s.sessionArtifacts {
		if artifact.DeletedAt != nil || !canAccessOwner(filter.Principal, artifact.OwnerUserID) {
			continue
		}
		if filter.SessionID != "" && artifact.SessionID != filter.SessionID {
			continue
		}
		if filter.Kind != "" && artifact.Kind != filter.Kind {
			continue
		}
		if filter.Status != "" && artifact.Status != filter.Status {
			continue
		}
		out = append(out, s.artifactWithContentsLocked(artifact))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ArtifactID > out[j].ArtifactID
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *MemoryStore) GetArtifactContent(
	ctx context.Context,
	principal UserPrincipal,
	artifactID string,
	ref string,
) (SessionArtifact, ArtifactContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	artifact, ok := s.sessionArtifacts[artifactID]
	if !ok || artifact.DeletedAt != nil || !canAccessOwner(principal, artifact.OwnerUserID) {
		return SessionArtifact{}, ArtifactContent{}, ErrNotFound
	}
	if ref == "" {
		ref = "main"
	}
	content, ok := s.sessionArtifactContents[artifactContentKey{
		ArtifactID: artifactID,
		Ref:        ref,
	}]
	if !ok {
		return SessionArtifact{}, ArtifactContent{}, ErrNotFound
	}
	return s.artifactWithContentsLocked(artifact), content, nil
}

func (s *MemoryStore) AttachSessionArtifact(
	ctx context.Context,
	principal UserPrincipal,
	req AttachArtifactRequest,
) (SessionArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	artifact, ok := s.sessionArtifacts[req.ArtifactID]
	if !ok || artifact.DeletedAt != nil || !canAccessOwner(principal, artifact.OwnerUserID) {
		return SessionArtifact{}, ErrNotFound
	}
	artifact.MessageID = strings.TrimSpace(req.MessageID)
	if req.SessionID != "" {
		artifact.SessionID = strings.TrimSpace(req.SessionID)
	}
	artifact.UpdatedAt = s.now().UTC()
	s.sessionArtifacts[artifact.ArtifactID] = artifact
	return s.artifactWithContentsLocked(artifact), nil
}

func (s *MemoryStore) artifactWithContentsLocked(artifact SessionArtifact) SessionArtifact {
	contents := make([]ArtifactContent, 0)
	for key, content := range s.sessionArtifactContents {
		if key.ArtifactID == artifact.ArtifactID {
			contents = append(contents, content)
		}
	}
	sort.Slice(contents, func(i, j int) bool {
		return contents[i].Ref < contents[j].Ref
	})
	artifact.Contents = contents
	artifact.SourceJSON = cloneRaw(artifact.SourceJSON)
	artifact.PayloadJSON = cloneRaw(artifact.PayloadJSON)
	return artifact
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) FailArtifactPublication(
	ctx context.Context,
	node Node,
	publicationID string,
	code string,
	message string,
) (ArtifactPublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	publication, ok := s.artifactPublications[publicationID]
	if !ok ||
		publication.NodeID != node.NodeID ||
		publication.OwnerUserID != node.OwnerUserID {
		return ArtifactPublication{}, ErrNotFound
	}
	if publication.Status == domain.ArtifactPublicationStatusAvailable {
		return publication, nil
	}
	if publication.Status == domain.ArtifactPublicationStatusFailed {
		if publication.ErrorCode != code {
			return ArtifactPublication{}, ErrConflict
		}
		return publication, nil
	}
	now := s.now().UTC()
	publication.Status = domain.ArtifactPublicationStatusFailed
	publication.ErrorCode = code
	publication.ErrorMessage = message
	publication.UpdatedAt = now
	s.artifactPublications[publication.PublicationID] = publication
	return publication, nil
}

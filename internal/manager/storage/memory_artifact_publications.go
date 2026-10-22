package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) PutArtifactPublication(
	ctx context.Context,
	publication ArtifactPublication,
) (ArtifactPublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.artifactPublications[publication.PublicationID]; ok {
		if !sameArtifactPublicationSource(existing, publication) {
			return ArtifactPublication{}, ErrConflict
		}
		return existing, nil
	}
	now := s.now().UTC()
	publication.CreatedAt = now
	publication.UpdatedAt = now
	publication.Origin = normalizeArtifactPublicationOrigin(publication.Origin)
	s.artifactPublications[publication.PublicationID] = publication
	return publication, nil
}

func (s *MemoryStore) GetArtifactPublication(
	ctx context.Context,
	principal UserPrincipal,
	publicationID string,
) (ArtifactPublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	publication, ok := s.artifactPublications[publicationID]
	if !ok || !canAccessOwner(principal, publication.OwnerUserID) {
		return ArtifactPublication{}, ErrNotFound
	}
	return publication, nil
}

func (s *MemoryStore) MarkArtifactPublicationUserFetch(
	ctx context.Context,
	publicationID string,
	requestedByUserID string,
	requestedPath string,
) (ArtifactPublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	publication, ok := s.artifactPublications[publicationID]
	if !ok || publication.OwnerUserID != requestedByUserID {
		return ArtifactPublication{}, ErrNotFound
	}
	if publication.Origin == domain.ArtifactPublicationOriginUserFetch &&
		publication.RequestedByUserID == requestedByUserID &&
		publication.RequestedPath == requestedPath {
		return publication, nil
	}
	publication.Origin = domain.ArtifactPublicationOriginUserFetch
	publication.RequestedByUserID = requestedByUserID
	publication.RequestedPath = requestedPath
	publication.UpdatedAt = s.now().UTC()
	s.artifactPublications[publicationID] = publication
	return publication, nil
}

func sameArtifactPublicationSource(left ArtifactPublication, right ArtifactPublication) bool {
	return left.OwnerUserID == right.OwnerUserID &&
		left.NodeID == right.NodeID &&
		left.AgentID == right.AgentID &&
		left.SessionID == right.SessionID &&
		left.Filename == right.Filename
}

func normalizeArtifactPublicationOrigin(origin string) string {
	if origin == "" {
		return domain.ArtifactPublicationOriginAgent
	}
	return origin
}

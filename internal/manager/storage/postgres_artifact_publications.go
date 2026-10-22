package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const artifactPublicationSelectColumns = `
	publication_id, owner_user_id, node_id, agent_id, session_id, filename, title,
	status, COALESCE(artifact_id, ''), error_code, error_message, origin,
	requested_by_user_id, requested_path, created_at, updated_at
`

func (s *PostgresStore) PutArtifactPublication(
	ctx context.Context,
	publication ArtifactPublication,
) (ArtifactPublication, error) {
	now := s.now().UTC()
	if publication.Status == "" {
		publication.Status = domain.ArtifactPublicationStatusQueued
	}
	publication.Origin = normalizeArtifactPublicationOrigin(publication.Origin)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO artifact_publications (
			publication_id, owner_user_id, node_id, agent_id, session_id, filename,
			title, status, artifact_id, error_code, error_message, origin,
			requested_by_user_id, requested_path, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12,$13,$14,$15,$15)
		ON CONFLICT (publication_id) DO NOTHING
	`, publication.PublicationID, publication.OwnerUserID, publication.NodeID,
		publication.AgentID, publication.SessionID, publication.Filename,
		publication.Title, publication.Status, publication.ArtifactID,
		publication.ErrorCode, publication.ErrorMessage, publication.Origin,
		publication.RequestedByUserID, publication.RequestedPath, now); err != nil {
		return ArtifactPublication{}, err
	}
	stored, err := scanArtifactPublication(s.db.QueryRowContext(ctx, `
		SELECT `+artifactPublicationSelectColumns+`
		FROM artifact_publications
		WHERE publication_id = $1
	`, publication.PublicationID))
	if err != nil {
		return ArtifactPublication{}, err
	}
	if !sameArtifactPublicationSource(stored, publication) {
		return ArtifactPublication{}, ErrConflict
	}
	return stored, nil
}

func (s *PostgresStore) GetArtifactPublication(
	ctx context.Context,
	principal UserPrincipal,
	publicationID string,
) (ArtifactPublication, error) {
	return scanArtifactPublication(s.db.QueryRowContext(ctx, `
		SELECT `+artifactPublicationSelectColumns+`
		FROM artifact_publications
		WHERE publication_id = $1 AND owner_user_id = $2
	`, publicationID, principal.User.UserID))
}

func (s *PostgresStore) MarkArtifactPublicationUserFetch(
	ctx context.Context,
	publicationID string,
	requestedByUserID string,
	requestedPath string,
) (ArtifactPublication, error) {
	return scanArtifactPublication(s.db.QueryRowContext(ctx, `
		UPDATE artifact_publications
		SET origin = $1, requested_by_user_id = $2, requested_path = $3, updated_at = $4
		WHERE publication_id = $5 AND owner_user_id = $2
		RETURNING `+artifactPublicationSelectColumns+`
	`, domain.ArtifactPublicationOriginUserFetch, requestedByUserID, requestedPath,
		s.now().UTC(), publicationID))
}

func scanArtifactPublication(row rowScanner) (ArtifactPublication, error) {
	var publication ArtifactPublication
	if err := row.Scan(
		&publication.PublicationID,
		&publication.OwnerUserID,
		&publication.NodeID,
		&publication.AgentID,
		&publication.SessionID,
		&publication.Filename,
		&publication.Title,
		&publication.Status,
		&publication.ArtifactID,
		&publication.ErrorCode,
		&publication.ErrorMessage,
		&publication.Origin,
		&publication.RequestedByUserID,
		&publication.RequestedPath,
		&publication.CreatedAt,
		&publication.UpdatedAt,
	); err != nil {
		return ArtifactPublication{}, mapSQLError(err)
	}
	return publication, nil
}

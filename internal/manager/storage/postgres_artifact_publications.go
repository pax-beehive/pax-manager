package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const artifactPublicationSelectColumns = `
	publication_id, owner_user_id, node_id, agent_id, session_id, filename, title,
	status, COALESCE(artifact_id, ''), error_code, error_message, created_at, updated_at
`

func (s *PostgresStore) PutArtifactPublication(
	ctx context.Context,
	publication ArtifactPublication,
) (ArtifactPublication, error) {
	now := s.now().UTC()
	if publication.Status == "" {
		publication.Status = domain.ArtifactPublicationStatusQueued
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO artifact_publications (
			publication_id, owner_user_id, node_id, agent_id, session_id, filename,
			title, status, artifact_id, error_code, error_message, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12,$12)
		ON CONFLICT (publication_id) DO NOTHING
	`, publication.PublicationID, publication.OwnerUserID, publication.NodeID,
		publication.AgentID, publication.SessionID, publication.Filename,
		publication.Title, publication.Status, publication.ArtifactID,
		publication.ErrorCode, publication.ErrorMessage, now); err != nil {
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
		&publication.CreatedAt,
		&publication.UpdatedAt,
	); err != nil {
		return ArtifactPublication{}, mapSQLError(err)
	}
	return publication, nil
}

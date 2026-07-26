package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) FailArtifactPublication(
	ctx context.Context,
	node Node,
	publicationID string,
	code string,
	message string,
) (ArtifactPublication, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactPublication{}, err
	}
	defer func() { _ = tx.Rollback() }()

	publication, err := scanArtifactPublication(tx.QueryRowContext(ctx, `
		SELECT `+artifactPublicationSelectColumns+`
		FROM artifact_publications
		WHERE publication_id = $1 AND node_id = $2 AND owner_user_id = $3
		FOR UPDATE
	`, publicationID, node.NodeID, node.OwnerUserID))
	if err != nil {
		return ArtifactPublication{}, err
	}
	if publication.Status == domain.ArtifactPublicationStatusAvailable {
		if err := tx.Commit(); err != nil {
			return ArtifactPublication{}, err
		}
		return publication, nil
	}
	if publication.Status == domain.ArtifactPublicationStatusFailed {
		if publication.ErrorCode != code {
			return ArtifactPublication{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return ArtifactPublication{}, err
		}
		return publication, nil
	}
	publication, err = scanArtifactPublication(tx.QueryRowContext(ctx, `
		UPDATE artifact_publications
		SET status = $1, error_code = $2, error_message = $3, updated_at = $4
		WHERE publication_id = $5
		RETURNING `+artifactPublicationSelectColumns+`
	`, domain.ArtifactPublicationStatusFailed, code, message, s.now().UTC(), publicationID))
	if err != nil {
		return ArtifactPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactPublication{}, err
	}
	return publication, nil
}

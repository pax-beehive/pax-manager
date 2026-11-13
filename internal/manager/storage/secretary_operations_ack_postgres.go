package storage

import (
	"context"
	"database/sql"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Explicit reconciliation can assign a fresh scan revision to unchanged text.
// Compare its hash inside PostgreSQL so an acknowledged large message never
// needs to cross the full-source input budget just to be skipped again.
func acknowledgeUnchangedLargeSecretaryMessageTx(ctx context.Context, tx *sql.Tx,
	job *domain.SecretaryJob, change secretaryChange,
) (bool, error) {
	if change.hash == "" || change.canonical != domain.SearchCanonicalizationVersion {
		return false, nil
	}
	var unchanged bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(
 'sha256:'||encode(sha256(convert_to(string_agg(CASE WHEN part_type='text'
 THEN COALESCE(text,'') ELSE '' END,'' ORDER BY part_index),'UTF8')),'hex')=$2
 AND bool_and(part_index>=0) AND count(*)=count(DISTINCT part_index),FALSE)
 FROM message_parts WHERE message_id=$1`, change.id, change.hash).Scan(&unchanged)
	if err != nil || !unchanged {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE secretary_message_state SET processed_change_revision=$3
 WHERE source_id=$1 AND message_id=$2 AND change_revision=$3 AND processed_owner_user_id=$4`,
		job.SourceID, change.id, change.revision, job.OwnerUserID)
	if err == nil {
		job.ThroughRevision = change.revision
	}
	return err == nil, err
}

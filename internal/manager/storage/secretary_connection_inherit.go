package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// Automatic superseding advances the epoch once. An explicit retry advances it
// again, so only an immediate superseded predecessor can transfer this budget.
func (s *MemoryStore) inheritSecretaryConnectionLocked(job domain.SecretaryJob) (int, time.Time) {
	var failures int
	var deadline time.Time
	for _, previous := range s.secretaryExecutionsLocked() {
		if previous.source == job.SourceID && previous.owner == job.OwnerUserID &&
			previous.settings == job.SettingsRevision && previous.epoch == job.RetryEpoch-1 &&
			previous.state == "superseded" && previous.connectionFailures > failures {
			failures, deadline = previous.connectionFailures, previous.retryAfter
		}
	}
	return failures, deadline
}

func inheritSecretaryConnectionTx(
	ctx context.Context,
	tx *sql.Tx,
	job domain.SecretaryJob,
) (int, sql.NullTime, error) {
	var failures int
	var deadline sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT connection_failures,retry_after FROM secretary_batches
 WHERE source_id=$1 AND owner_user_id=$2 AND settings_revision=$3 AND retry_epoch=$4-1
 AND state='superseded' AND connection_failures>0
 ORDER BY connection_failures DESC,updated_at DESC,batch_id DESC LIMIT 1`,
		job.SourceID, job.OwnerUserID, job.SettingsRevision, job.RetryEpoch).
		Scan(&failures, &deadline)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return failures, deadline, err
}

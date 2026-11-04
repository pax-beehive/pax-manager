package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) blockSecretaryPromptFailure(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	_, err := s.db.ExecContext(ctx, `WITH stopped AS (
 UPDATE secretary_batches SET state='blocked',updated_at=NOW()
 WHERE source_id=$1 AND owner_user_id=$2 AND lease_token=$3 AND current_attempt_id='' AND state='pending'
 AND EXISTS(SELECT 1 FROM secretary_jobs j WHERE j.source_id=secretary_batches.source_id AND j.lease_token=$3 AND j.lease_until>NOW())
 RETURNING source_id)
 UPDATE secretary_jobs j SET execution_blocked=TRUE FROM stopped
 WHERE j.source_id=stopped.source_id AND j.lease_token=$3 AND j.lease_until>NOW()`, job.SourceID, job.OwnerUserID, job.LeaseToken)
	return err
}

func (s *MemoryStore) blockSecretaryPromptFailureLocked(job domain.SecretaryJob) {
	for _, batch := range s.secretaryExecutionsLocked() {
		if batch.source == job.SourceID && batch.owner == job.OwnerUserID &&
			batch.lease == job.LeaseToken &&
			batch.state == "pending" &&
			len(batch.attempts) == 0 {
			batch.state = "blocked"
			s.secretaryLocked().jobs[job.SourceID].executionBlocked = true
		}
	}
}

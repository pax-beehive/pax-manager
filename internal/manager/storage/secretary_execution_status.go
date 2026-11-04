package storage

import "time"

func secretaryExecutionStatus(batch, attempt string, leased bool, pending int) (string, string) {
	switch batch {
	case "running":
		if !leased {
			return "needs_recovery", "lease_expired"
		}
		return "running", ""
	case "output_received":
		return "output_received", ""
	case "unknown":
		return "unknown", "execution_unknown"
	case "blocked":
		switch attempt {
		case "invalid":
			return "blocked", "invalid_output"
		case "failed":
			return "blocked", "execution_failed"
		case "canceled":
			return "blocked", "canceled"
		default:
			return "blocked", "retry_required"
		}
	case "pending":
		return "queued", ""
	default:
		if pending > 0 {
			return "waiting", ""
		}
		return "", ""
	}
}

func (s *MemoryStore) memorySecretaryExecutionStatusLocked(
	owner string,
	settings int64,
	pending int,
) (string, string) {
	var latest time.Time
	var selectedID, selectedState, selectedAttempt string
	selectedLease := false
	state := s.secretaryLocked()
	for id, batch := range state.executions {
		job := state.jobs[batch.source]
		if !currentSecretaryStatusBatch(batch, job, owner, settings) {
			continue
		}
		updated := job.updated
		attempt := ""
		if len(batch.attempts) > 0 {
			last := batch.attempts[len(batch.attempts)-1]
			updated, attempt = last.UpdatedAt, last.State
		}
		if selectedID != "" &&
			(updated.Before(latest) || updated.Equal(latest) && id < selectedID) {
			continue
		}
		latest, selectedID, selectedState, selectedAttempt = updated, id, batch.state, attempt
		selectedLease = job.work.LeaseToken != "" && job.work.LeaseToken == batch.lease &&
			job.leaseUntil.After(s.now())
	}
	return secretaryExecutionStatus(selectedState, selectedAttempt, selectedLease, pending)
}

func currentSecretaryStatusBatch(
	batch *memorySecretaryExecutionBatch,
	job *memorySecretaryJob,
	owner string,
	settings int64,
) bool {
	return batch.owner == owner && batch.settings == settings && job != nil &&
		job.work.OwnerUserID == owner &&
		batch.epoch == job.work.RetryEpoch &&
		job.desired > job.processed &&
		batch.state != "committed" &&
		batch.state != "superseded"
}

const secretaryStatusSQL = `WITH queue AS (
 SELECT count(*) FILTER(WHERE desired_revision>processed_revision) AS pending,
 COALESCE(bool_or(lease_token<>'' AND lease_until>NOW()),false) AS processing,max(completed_at) AS completed,
 COALESCE((SELECT last_error FROM secretary_jobs WHERE owner_user_id=$1 AND last_error<>'' ORDER BY updated_at DESC LIMIT 1),'') AS failure
 FROM secretary_jobs WHERE owner_user_id=$1
), latest AS (
 SELECT b.state AS batch_state,COALESCE(a.state,'') AS attempt_state,
 COALESCE(j.lease_token<>'' AND j.lease_token=b.lease_token AND j.lease_until>NOW(),false) AS leased
 FROM secretary_batches b JOIN secretary_jobs j ON j.source_id=b.source_id AND j.owner_user_id=b.owner_user_id
 LEFT JOIN secretary_attempts a ON a.attempt_id=b.current_attempt_id
 WHERE b.owner_user_id=$1 AND b.settings_revision=$2 AND b.retry_epoch=j.retry_epoch
 AND j.desired_revision>j.processed_revision
 AND b.state IN ('pending','running','output_received','unknown','blocked')
 ORDER BY b.updated_at DESC,b.batch_id DESC LIMIT 1
)
 SELECT queue.pending,queue.processing,queue.completed,queue.failure,
 COALESCE(latest.batch_state,''),COALESCE(latest.attempt_state,''),COALESCE(latest.leased,false)
 FROM queue LEFT JOIN latest ON true`

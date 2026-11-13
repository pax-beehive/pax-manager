package storage

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	_ domain.SecretaryAutoRestartStore = (*MemoryStore)(nil)
	_ domain.SecretaryAutoRestartStore = (*PostgresStore)(nil)
)

// secretaryRestartJobSQL starts a new retry epoch for one source. The previous
// batch and attempt stay isolated in the old epoch, so late output cannot commit.
const secretaryRestartJobSQL = `UPDATE secretary_jobs SET retry_epoch=retry_epoch+1,retry_generation=retry_generation+1,
 paused=FALSE,execution_blocked=FALSE,control_revision=control_revision+1,lease_token='',lease_until=NULL,
 available_at=NOW(),updated_at=NOW()-INTERVAL '30 seconds',attempts=0,last_error='',last_validation_failure='',
 auto_retry_key='',auto_retry_count=0`

// The stored count only applies while the job is still in the epoch created by
// its last automatic restart and has not published since. Every explicit retry
// moves to another epoch, so no other write path has to reset the budget.
const secretaryAutoRestartCountSQL = `CASE WHEN j.auto_restart_epoch=j.retry_epoch AND (j.completed_at IS NULL
 OR j.auto_restarted_at IS NULL OR j.completed_at<=j.auto_restarted_at) THEN j.auto_restart_count ELSE 0 END`

const secretaryAutoRestartCandidatesSQL = `SELECT j.owner_user_id,j.source_id::text FROM secretary_jobs j
 JOIN secretary_settings cfg ON cfg.owner_user_id=j.owner_user_id AND cfg.enabled
 WHERE j.execution_blocked AND NOT j.paused
 AND (j.lease_token='' OR j.lease_until IS NULL OR j.lease_until<=NOW())
 AND ` + secretaryAutoRestartCountSQL + `<$1
 AND EXISTS(SELECT 1 FROM secretary_batches b JOIN secretary_attempts a
 ON a.attempt_id=b.current_attempt_id AND a.batch_id=b.batch_id
 WHERE b.source_id=j.source_id AND b.owner_user_id=j.owner_user_id AND b.settings_revision=cfg.revision
 AND b.retry_epoch=j.retry_epoch AND b.state IN ('unknown','blocked','pending')
 AND CASE WHEN b.state='unknown' THEN a.created_at ELSE a.updated_at END<=$2)
 ORDER BY j.source_id LIMIT $3`

func secretaryAutoRestartLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 100
	}
	return limit
}

// secretaryAutoRestartDue reports whether a task can no longer progress on its
// own and has waited long enough for its next automatic restart.
//
// The job must be unclaimable: a job that is not blocked is still owned by the
// ordinary bounded retries. An unknown outcome is measured from the start of
// its attempt, because later rechecks of the same attempt refresh every other
// timestamp. Other batches are measured from their last failure. Deterministic
// input failures are skipped: repeating them cannot succeed.
func secretaryAutoRestartDue(record secretaryTaskRecord, now time.Time) (string, bool) {
	task := secretaryTaskView(record, now)
	if !record.enabled || !record.blocked || task.Paused || task.LeaseActive ||
		task.AttemptID == "" || task.ExecutionState == "completed" ||
		task.ErrorCode == "input_size_limit" ||
		record.autoRestarts >= domain.SecretaryAutoRestartMax {
		return "", false
	}
	var since time.Time
	switch record.batchState {
	case "unknown":
		since = record.attemptCreated
	case "blocked", "pending":
		// A pending batch behind a blocked job is never claimed either; a manual
		// recheck that ends in a retryable failure can leave it that way.
		since = record.attemptUpdated
	default:
		return "", false
	}
	if since.IsZero() ||
		now.Before(since.Add(domain.SecretaryAutoRestartDelay(record.autoRestarts))) {
		return "", false
	}
	return record.batchState, true
}

func (j *memorySecretaryJob) consecutiveAutoRestarts() int {
	if j.autoRestartedAt.IsZero() || j.autoRestartEpoch != j.work.RetryEpoch ||
		(j.completed != nil && j.completed.After(j.autoRestartedAt)) {
		return 0
	}
	return j.autoRestartCount
}

func (s *MemoryStore) RestartStalledSecretaryTasks(
	ctx context.Context,
	limit int,
) ([]domain.SecretaryTaskRestart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit = secretaryAutoRestartLimit(limit)
	jobs := s.secretaryLocked().jobs
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var restarted []domain.SecretaryTaskRestart
	for _, id := range ids {
		if len(restarted) >= limit {
			break
		}
		job := jobs[id]
		owner := job.work.OwnerUserID
		record, err := s.memorySecretaryTaskLocked(owner, id)
		if err != nil {
			continue
		}
		state, due := secretaryAutoRestartDue(record, s.now())
		if !due {
			continue
		}
		s.retryMemorySecretaryTaskLocked(job)
		job.controlRevision++
		job.autoRestartCount, job.autoRestartEpoch = record.autoRestarts+1, job.work.RetryEpoch
		job.autoRestartedAt = s.now()
		restarted = append(restarted, domain.SecretaryTaskRestart{
			OwnerUserID: owner, TaskID: id, PreviousState: state, Restarts: job.autoRestartCount,
		})
	}
	return restarted, nil
}

func (s *PostgresStore) RestartStalledSecretaryTasks(
	ctx context.Context,
	limit int,
) ([]domain.SecretaryTaskRestart, error) {
	// The earliest possible restart bounds the scan; the exact decision is
	// repeated under the task lock below.
	rows, err := s.db.QueryContext(
		ctx,
		secretaryAutoRestartCandidatesSQL,
		domain.SecretaryAutoRestartMax,
		s.now().Add(-domain.SecretaryAutoRestartDelay(0)),
		secretaryAutoRestartLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	type candidate struct{ owner, id string }
	var candidates []candidate
	for rows.Next() {
		var next candidate
		if err = rows.Scan(&next.owner, &next.id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, next)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	var restarted []domain.SecretaryTaskRestart
	var failures []error
	for _, next := range candidates {
		restart, ok, restartErr := s.restartStalledSecretaryTask(ctx, next.owner, next.id)
		if restartErr != nil {
			// One unreadable task must not keep the others stalled.
			failures = append(failures, restartErr)
			continue
		}
		if ok {
			restarted = append(restarted, restart)
		}
	}
	return restarted, errors.Join(failures...)
}

func (s *PostgresStore) restartStalledSecretaryTask(
	ctx context.Context,
	owner, id string,
) (domain.SecretaryTaskRestart, bool, error) {
	tx, record, err := lockSecretaryTaskTx(ctx, s, owner, id)
	if errors.Is(err, ErrNotFound) {
		return domain.SecretaryTaskRestart{}, false, nil
	}
	if err != nil {
		return domain.SecretaryTaskRestart{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, due := secretaryAutoRestartDue(record, s.now())
	if !due {
		return domain.SecretaryTaskRestart{}, false, nil
	}
	restarts := record.autoRestarts + 1
	_, err = tx.ExecContext(ctx, secretaryRestartJobSQL+`,auto_restart_count=$2,
 auto_restart_epoch=retry_epoch+1,auto_restarted_at=NOW() WHERE source_id=$1`, id, restarts)
	if err == nil {
		err = retrySecretaryOperationTx(ctx, tx, owner, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return domain.SecretaryTaskRestart{}, false, err
	}
	return domain.SecretaryTaskRestart{
		OwnerUserID: owner, TaskID: id, PreviousState: state, Restarts: restarts,
	}, true, nil
}

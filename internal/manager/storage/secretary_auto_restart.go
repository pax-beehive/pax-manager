package storage

import (
	"context"
	"errors"
	"sort"
	"strconv"
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
// its last automatic restart, has not published since, and its settings have
// not changed since. Every explicit retry moves to another epoch, so no other
// write path has to reset the budget.
const secretaryAutoRestartCountSQL = `CASE WHEN j.auto_restart_epoch=j.retry_epoch AND j.auto_restarted_at IS NOT NULL
 AND (j.completed_at IS NULL OR j.completed_at<=j.auto_restarted_at)
 AND cfg.updated_at<=j.auto_restarted_at THEN j.auto_restart_count ELSE 0 END`

// A guided operation that needs attention keeps its source out of ordinary claims.
const secretaryOperationStalledSQL = `EXISTS(SELECT 1 FROM secretary_operations stalled
 WHERE stalled.owner_user_id=j.owner_user_id AND stalled.source_id=j.source_id
 AND stalled.kind='guided_summary' AND stalled.status='needs_attention')`

// The candidate query mirrors secretaryAutoRecoveryDue closely enough that a
// page is not filled with tasks that cannot be recovered. The decision itself
// is always repeated under the task lock.
const secretaryAutoRestartCandidatesSQL = `SELECT DISTINCT j.source_id,j.owner_user_id FROM secretary_jobs j
 JOIN secretary_settings cfg ON cfg.owner_user_id=j.owner_user_id AND cfg.enabled
 JOIN agents source ON source.agent_id=j.source_agent_id AND source.owner_user_id=j.owner_user_id AND source.deleted_at IS NULL
 JOIN agent_sessions s ON s.id=j.source_id AND s.agent_id=j.source_agent_id AND s.session_id=j.source_session_id AND s.archived_at IS NULL
 LEFT JOIN nodes source_node ON source_node.node_id=source.node_id LEFT JOIN nodes session_node ON session_node.node_id=s.node_id
 JOIN secretary_batches b ON b.source_id=j.source_id AND b.owner_user_id=j.owner_user_id
 AND b.settings_revision=cfg.revision AND b.retry_epoch=j.retry_epoch
 AND b.state IN ('unknown','running','blocked','pending','output_received')
 JOIN secretary_attempts a ON a.attempt_id=b.current_attempt_id AND a.batch_id=b.batch_id
 WHERE j.source_id>$1::bigint AND NOT j.paused
 AND (j.lease_token='' OR j.lease_until IS NULL OR j.lease_until<=NOW())
 AND source_node.deleted_at IS NULL AND session_node.deleted_at IS NULL
 AND COALESCE(s.source,'')<>'secretary_internal'
 AND NOT EXISTS(SELECT 1 FROM secretary_internal_sessions i WHERE i.agent_id=s.agent_id AND i.session_id=s.session_id)
 AND (j.execution_blocked OR (b.state='blocked' AND b.connection_failures>$2::int) OR ` + secretaryOperationStalledSQL + `)
 AND (b.state='output_received' OR ` + secretaryAutoRestartCountSQL + `<
 CASE WHEN b.state='blocked' AND b.connection_failures<=$2::int THEN $3::int ELSE $4::int END)
 AND CASE WHEN b.state IN ('unknown','running') THEN a.created_at ELSE a.updated_at END
 +($5::double precision*power(4,CASE WHEN b.state='output_received' THEN 0 ELSE ` + secretaryAutoRestartCountSQL + ` END))
 *INTERVAL '1 millisecond'<=$6::timestamptz
 ORDER BY j.source_id LIMIT $7::int`

const secretaryAutoRestartPage = 100

type secretaryAutoRecovery int

const (
	secretaryAutoRecoveryNone secretaryAutoRecovery = iota
	// Start a new retry epoch, exactly like the explicit task retry.
	secretaryAutoRecoveryRestart
	// Release saved output for publishing. No model call is needed.
	secretaryAutoRecoveryResume
)

func secretaryAutoRestartLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 100
	}
	return limit
}

func secretaryAutoRestartWaited(since, now time.Time, restarts int) bool {
	return !since.IsZero() && !now.Before(since.Add(domain.SecretaryAutoRestartDelay(restarts)))
}

// secretaryAutoRecoveryDue decides whether a task can no longer progress on
// its own and has waited long enough for its next automatic recovery.
//
// Ordinary claims skip a blocked job, an exhausted connection budget and a
// guided operation that needs attention. A new source message clears only the
// first flag, so each reason counts on its own. A task that claims can still
// reach is left to the ordinary bounded retries.
//
// An unknown or interrupted attempt is measured from its start, because later
// rechecks of the same attempt refresh every other timestamp. Other batches
// are measured from their last failure. Deterministic input failures are
// skipped: repeating them cannot succeed.
func secretaryAutoRecoveryDue(record secretaryTaskRecord, now time.Time) secretaryAutoRecovery {
	task := secretaryTaskView(record, now)
	if !record.enabled || task.Paused || task.LeaseActive || task.AttemptID == "" ||
		task.ExecutionState == "completed" {
		return secretaryAutoRecoveryNone
	}
	exhausted := secretaryConnectionExhausted(record.batchState, task.ConnectionFailures)
	if !record.blocked && !exhausted && !record.operationStalled {
		return secretaryAutoRecoveryNone
	}
	var since time.Time
	switch record.batchState {
	case "output_received":
		if record.blocked && secretaryAutoRestartWaited(record.attemptUpdated, now, 0) {
			return secretaryAutoRecoveryResume
		}
		return secretaryAutoRecoveryNone
	case "unknown", "running":
		since = record.attemptCreated
	case "blocked", "pending":
		// A pending batch behind a blocked job is never claimed either; a manual
		// recheck that ends in a retryable failure can leave it that way.
		since = record.attemptUpdated
	default:
		return secretaryAutoRecoveryNone
	}
	if task.ErrorCode == "input_size_limit" {
		return secretaryAutoRecoveryNone
	}
	attemptsExhausted := record.batchState == "blocked" && !exhausted
	if record.autoRestarts >= domain.SecretaryAutoRestartBudget(attemptsExhausted) ||
		!secretaryAutoRestartWaited(since, now, record.autoRestarts) {
		return secretaryAutoRecoveryNone
	}
	return secretaryAutoRecoveryRestart
}

func (j *memorySecretaryJob) consecutiveAutoRestarts(settingsUpdated time.Time) int {
	if j.autoRestartedAt.IsZero() || j.autoRestartEpoch != j.work.RetryEpoch ||
		(j.completed != nil && j.completed.After(j.autoRestartedAt)) ||
		settingsUpdated.After(j.autoRestartedAt) {
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
	// Apply pending source changes first, as a claim does: a new message can
	// unblock a job, and that must be visible to the decision below.
	s.refreshSecretaryLocked()
	jobs := s.secretaryLocked().jobs
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var recovered []domain.SecretaryTaskRestart
	for _, id := range ids {
		if len(recovered) >= limit {
			break
		}
		job := jobs[id]
		owner := job.work.OwnerUserID
		record, err := s.memorySecretaryTaskLocked(owner, id)
		if err != nil {
			continue
		}
		result := domain.SecretaryTaskRestart{
			OwnerUserID: owner, TaskID: id,
			PreviousState: record.batchState, Restarts: record.autoRestarts,
		}
		switch secretaryAutoRecoveryDue(record, s.now()) {
		case secretaryAutoRecoveryResume:
			job.executionBlocked, job.available = false, s.now()
			result.Resumed = true
		case secretaryAutoRecoveryRestart:
			s.retryMemorySecretaryTaskLocked(job)
			result.Restarts++
			job.autoRestartCount, job.autoRestartEpoch = result.Restarts, job.work.RetryEpoch
			job.autoRestartedAt = s.now()
		default:
			continue
		}
		job.controlRevision++
		recovered = append(recovered, result)
	}
	return recovered, nil
}

type secretaryAutoRestartCandidate struct {
	source int64
	owner  string
}

func (s *PostgresStore) secretaryAutoRestartCandidates(
	ctx context.Context,
	after int64,
) ([]secretaryAutoRestartCandidate, error) {
	rows, err := s.db.QueryContext(
		ctx,
		secretaryAutoRestartCandidatesSQL,
		after,
		domain.SecretaryConnectionMaxRetries,
		domain.SecretaryAutoRestartBudget(true),
		domain.SecretaryAutoRestartBudget(false),
		float64(domain.SecretaryAutoRestartDelay(0).Milliseconds()),
		s.now(),
		secretaryAutoRestartPage,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var page []secretaryAutoRestartCandidate
	for rows.Next() {
		var next secretaryAutoRestartCandidate
		if err = rows.Scan(&next.source, &next.owner); err != nil {
			return nil, err
		}
		page = append(page, next)
	}
	return page, rows.Err()
}

func (s *PostgresStore) RestartStalledSecretaryTasks(
	ctx context.Context,
	limit int,
) ([]domain.SecretaryTaskRestart, error) {
	limit = secretaryAutoRestartLimit(limit)
	var recovered []domain.SecretaryTaskRestart
	var failures []error
	// Walk every candidate by key: a task that cannot be recovered right now
	// must not keep later tasks out of the page forever.
	for after := int64(0); len(recovered) < limit; {
		page, err := s.secretaryAutoRestartCandidates(ctx, after)
		if err != nil {
			return recovered, errors.Join(append(failures, err)...)
		}
		for _, next := range page {
			after = next.source
			if len(recovered) >= limit {
				break
			}
			result, ok, recoverErr := s.recoverStalledSecretaryTask(
				ctx, next.owner, strconv.FormatInt(next.source, 10))
			if recoverErr != nil {
				// One unreadable task must not keep the others stalled.
				failures = append(failures, recoverErr)
				continue
			}
			if ok {
				recovered = append(recovered, result)
			}
		}
		if len(page) < secretaryAutoRestartPage {
			break
		}
	}
	return recovered, errors.Join(failures...)
}

func (s *PostgresStore) recoverStalledSecretaryTask(
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
	result := domain.SecretaryTaskRestart{
		OwnerUserID: owner, TaskID: id,
		PreviousState: record.batchState, Restarts: record.autoRestarts,
	}
	switch secretaryAutoRecoveryDue(record, s.now()) {
	case secretaryAutoRecoveryResume:
		result.Resumed = true
		_, err = tx.ExecContext(
			ctx,
			`UPDATE secretary_jobs SET execution_blocked=FALSE,available_at=NOW(),
 control_revision=control_revision+1 WHERE source_id=$1`,
			id,
		)
	case secretaryAutoRecoveryRestart:
		result.Restarts++
		_, err = tx.ExecContext(ctx, secretaryRestartJobSQL+`,auto_restart_count=$2,
 auto_restart_epoch=retry_epoch+1,auto_restarted_at=NOW() WHERE source_id=$1`, id, result.Restarts)
		if err == nil {
			err = retrySecretaryOperationTx(ctx, tx, owner, id)
		}
	default:
		return domain.SecretaryTaskRestart{}, false, nil
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return domain.SecretaryTaskRestart{}, false, err
	}
	return result, true, nil
}

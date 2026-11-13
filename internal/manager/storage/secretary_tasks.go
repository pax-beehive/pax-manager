package storage

import (
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryModelConnectionFailure = "secretary execution outcome is unknown\nInternal error: The model connection failed. This request did not complete. You can retry."

type secretaryTaskRecord struct {
	task               domain.SecretaryTask
	desired, processed int64
	available          time.Time
	retryAfter         time.Time
	batchState         string
	attemptState       string
	notDispatched      bool
	failure            string
	sourceRuntime      string
	enabled            bool
	blocked            bool
	operationPending   bool
	// Current attempt timestamps and the consecutive automatic restart count,
	// used only to decide automatic recovery.
	attemptCreated time.Time
	attemptUpdated time.Time
	autoRestarts   int
}

func secretaryTaskQuery(owner, scope string, query domain.SecretaryTaskQuery) (string, int, error) {
	if owner == "" {
		return "", 0, ErrUnauthorized
	}
	if len(query.Cursor) > 4096 {
		return "", 0, domain.ErrInvalidCursor
	}
	return summaryListQuery("secretary-tasks:"+owner+":"+scope,
		domain.SummaryListQuery{Limit: query.Limit, Cursor: query.Cursor})
}

func secretaryTaskNextCursor(owner, scope, after string) string {
	return summaryNextCursor("secretary-tasks:"+owner+":"+scope, after)
}

func secretaryTaskID(id string) (int64, error) {
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != id {
		return 0, ErrNotFound
	}
	return value, nil
}

func secretaryTaskAfter(owner string, query domain.SecretaryTaskQuery) (int64, int, error) {
	after, limit, err := secretaryTaskQuery(owner, "list", query)
	if err != nil || after == "" {
		return 0, limit, err
	}
	id, err := secretaryTaskID(after)
	if err != nil {
		return 0, 0, domain.ErrInvalidCursor
	}
	return id, limit, nil
}

func secretaryTaskAttemptAfter(
	owner, id string,
	query domain.SecretaryTaskQuery,
) (time.Time, string, int, error) {
	after, limit, err := secretaryTaskQuery(owner, "attempts:"+id, query)
	if err != nil || after == "" {
		return time.Time{}, "", limit, err
	}
	parts := strings.Split(after, "\x00")
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", 0, domain.ErrInvalidCursor
	}
	created, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil || created.IsZero() {
		return time.Time{}, "", 0, domain.ErrInvalidCursor
	}
	return created, parts[1], limit, nil
}

func secretaryTaskAttemptCursor(owner, id string, attempt domain.SecretaryTaskAttempt) string {
	return secretaryTaskNextCursor(owner, "attempts:"+id,
		attempt.CreatedAt.Format(time.RFC3339Nano)+"\x00"+attempt.AttemptID)
}

func secretaryTaskFailure(state, attempt, failure string, notDispatched bool) (string, string) {
	if failure == domain.ErrSecretarySourceIncomplete.Error() {
		return "source_incomplete", "Waiting for the source turn to finish before summarizing."
	}
	if failure == secretaryModelConnectionFailure {
		return "model_connection_failed", "The model connection failed. Check the existing result before retrying."
	}
	if state == "unknown" || attempt == "unknown" {
		return "execution_unknown", "The execution outcome is unknown. Check the existing result before retrying."
	}
	if strings.HasPrefix(failure, secretaryInputLimitPrefix) {
		return "input_size_limit", "This source exceeds the supported input size. Reduce it or retry after the input limit changes."
	}
	if notDispatched {
		if state == "blocked" {
			return "connection_retries_exhausted", "Connection retries are exhausted. Retry this task to start a new retry budget."
		}
		return "connection_backoff", "The request was not dispatched. Waiting before reconnecting."
	}
	switch attempt {
	case "invalid":
		return "invalid_output", "The returned summary could not be validated."
	case "failed":
		return "execution_failed", "The summary task failed."
	case "canceled":
		return "retry_required", "The previous execution was canceled or superseded."
	}
	if state == "blocked" || failure != "" {
		return "retry_required", "The summary task needs attention."
	}
	return "", ""
}

func secretaryTaskView(record secretaryTaskRecord, now time.Time) domain.SecretaryTask {
	task := record.task
	task.NextRetryAt = nil
	task.ExecutionState = secretaryTaskState(record, now)
	task.ErrorCode, task.ErrorMessage = secretaryTaskFailure(record.batchState, record.attemptState,
		record.failure, record.notDispatched)
	if task.ExecutionState == "completed" {
		task.ErrorCode, task.ErrorMessage = "", ""
		return task
	}
	deadline := record.available
	if record.retryAfter.After(deadline) {
		deadline = record.retryAfter
	}
	if !task.Paused && !task.LeaseActive && task.ExecutionState == "waiting_retry" &&
		deadline.After(now) {
		task.NextRetryAt = &deadline
	}
	return task
}

func secretaryTaskState(record secretaryTaskRecord, now time.Time) string {
	if record.processed >= record.desired && !record.operationPending {
		return "completed"
	}
	switch record.batchState {
	case "unknown", "output_received", "blocked":
		return record.batchState
	case "running":
		if record.task.LeaseActive {
			return "running"
		}
		return "needs_recovery"
	}
	if record.blocked {
		return "blocked"
	}
	if record.failure == domain.ErrSecretarySourceIncomplete.Error() {
		return "waiting_source"
	}
	if !record.enabled ||
		record.sourceRuntime != "" && record.sourceRuntime != domain.RuntimeStatusIdle {
		return "waiting_source"
	}
	if record.available.After(now) || record.retryAfter.After(now) {
		return "waiting_retry"
	}
	return "queued"
}

func validateSecretaryTaskAction(
	task domain.SecretaryTask,
	action domain.SecretaryTaskAction,
) error {
	if action.ExpectedRevision != task.Revision || action.BatchID != task.BatchID ||
		action.AttemptID != task.AttemptID || action.RetryEpoch != task.RetryEpoch ||
		action.SettingsRevision != task.SettingsRevision {
		return ErrConflict
	}
	switch action.Action {
	case "pause", "resume":
		return nil
	case "retry", "recheck":
		if task.LeaseActive || task.ExecutionState == "completed" {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	if action.Action == "retry" &&
		(task.ExecutionState == "unknown" || task.ExecutionState == "needs_recovery") && !action.ConfirmRestart {
		return ErrConflict
	}
	return nil
}

func secretaryRecoveryLease(value time.Duration) time.Duration {
	if value <= 0 || value > 5*time.Minute {
		return time.Minute
	}
	return value
}

func secretaryExecutionSessionID(owner, attempt string) string {
	return domain.SecretaryInternalSessionPrefix +
		strings.TrimPrefix(domain.SearchContentHash(owner+"\x00"+attempt), "sha256:")[:32]
}

func secretaryTaskSessionTitle(session domain.AgentSession) string {
	if session.CustomSessionName != "" {
		return session.CustomSessionName
	}
	if session.SessionName != "" {
		return session.SessionName
	}
	return session.SessionID
}

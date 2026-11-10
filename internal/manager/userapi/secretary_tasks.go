package userapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type SecretaryTaskRecovery func(context.Context, string, string, domain.SecretaryTaskAction) (string, error)

type SecretaryTaskService struct {
	principal PrincipalResolver
	store     domain.SecretaryTaskStore
	recovery  SecretaryTaskRecovery
}

type SecretaryTaskActionResponse struct {
	Task    domain.SecretaryTask `json:"task"`
	Outcome string               `json:"outcome,omitempty"`
}

func NewSecretaryTaskService(
	principal PrincipalResolver,
	store domain.SecretaryTaskStore,
	recovery SecretaryTaskRecovery,
) *SecretaryTaskService {
	return &SecretaryTaskService{principal: principal, store: store, recovery: recovery}
}

func (s *SecretaryTaskService) owner(
	ctx context.Context,
	meta auth.RequestMetadata,
) (string, error) {
	if s == nil || s.principal == nil {
		return "", domain.ErrUnauthorized
	}
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return "", err
	}
	if principal.User.UserID == "" {
		return "", domain.ErrUnauthorized
	}
	if s.store == nil {
		return "", searchHTTPError(http.StatusServiceUnavailable, "Secretary tasks are unavailable")
	}
	return principal.User.UserID, nil
}

func (s *SecretaryTaskService) List(
	ctx context.Context,
	meta auth.RequestMetadata,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskPage, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	query, err = secretaryTaskQuery(query)
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	page, err := s.store.ListSecretaryTasks(ctx, owner, query)
	if err != nil {
		return domain.SecretaryTaskPage{}, secretaryTaskError(err)
	}
	if page.Items == nil {
		page.Items = []domain.SecretaryTask{}
	}
	for i := range page.Items {
		page.Items[i] = safeSecretaryTask(page.Items[i])
	}
	return page, nil
}

func (s *SecretaryTaskService) Get(
	ctx context.Context,
	meta auth.RequestMetadata,
	id string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskDetail, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	query, err = secretaryTaskQuery(query)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	if !validSecretaryTaskID(id, false) {
		return domain.SecretaryTaskDetail{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid Secretary task identifier",
		)
	}
	detail, err := s.store.GetSecretaryTask(ctx, owner, id, query)
	if err != nil {
		return domain.SecretaryTaskDetail{}, secretaryTaskError(err)
	}
	detail.Task = safeSecretaryTask(detail.Task)
	if detail.Attempts == nil {
		detail.Attempts = []domain.SecretaryTaskAttempt{}
	}
	for i := range detail.Attempts {
		detail.Attempts[i].ErrorCode, detail.Attempts[i].ErrorMessage = safeSecretaryTaskFailure(
			detail.Attempts[i].ErrorCode,
		)
	}
	return detail, nil
}

func (s *SecretaryTaskService) Control(
	ctx context.Context,
	meta auth.RequestMetadata,
	id string,
	action domain.SecretaryTaskAction,
) (SecretaryTaskActionResponse, error) {
	owner, err := s.owner(ctx, meta)
	if err != nil {
		return SecretaryTaskActionResponse{}, err
	}
	if !validSecretaryTaskAction(id, action) {
		return SecretaryTaskActionResponse{}, searchHTTPError(
			http.StatusBadRequest,
			"Invalid Secretary task action or observed revision",
		)
	}
	if action.Action == "recheck" {
		return s.recheck(ctx, meta, owner, id, action)
	}
	if action.Action == "retry" {
		detail, readErr := s.store.GetSecretaryTask(
			ctx,
			owner,
			id,
			domain.SecretaryTaskQuery{Limit: 1},
		)
		if readErr != nil {
			return SecretaryTaskActionResponse{}, secretaryTaskError(readErr)
		}
		if secretaryTaskRestartNeedsConfirmation(detail.Task) && !action.ConfirmRestart {
			return SecretaryTaskActionResponse{}, searchHTTPError(
				http.StatusConflict,
				"The previous attempt may have executed. Confirm restart to allow another attempt and possible duplicate work.",
			)
		}
	}
	task, err := s.store.ControlSecretaryTask(ctx, owner, id, action)
	if err != nil {
		return SecretaryTaskActionResponse{}, secretaryTaskError(err)
	}
	outcome := map[string]string{"pause": "paused", "resume": "resumed", "retry": "queued"}[action.Action]
	return SecretaryTaskActionResponse{Task: safeSecretaryTask(task), Outcome: outcome}, nil
}

func (s *SecretaryTaskService) recheck(
	ctx context.Context,
	meta auth.RequestMetadata,
	owner, id string,
	action domain.SecretaryTaskAction,
) (SecretaryTaskActionResponse, error) {
	if s.recovery == nil {
		return SecretaryTaskActionResponse{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"Secretary result checks are unavailable",
		)
	}
	outcome, err := s.recovery(ctx, owner, id, action)
	if err != nil {
		return SecretaryTaskActionResponse{}, secretaryTaskError(err)
	}
	detail, err := s.Get(ctx, meta, id, domain.SecretaryTaskQuery{Limit: 1})
	if err != nil {
		return SecretaryTaskActionResponse{}, err
	}
	switch outcome {
	case "recovered", "still_unknown", "result_invalid", "result_failed", "output_saved":
	default:
		outcome = "status_refreshed"
	}
	return SecretaryTaskActionResponse{Task: detail.Task, Outcome: outcome}, nil
}

func secretaryTaskRestartNeedsConfirmation(task domain.SecretaryTask) bool {
	return task.ExecutionState == "unknown" || task.ExecutionState == "needs_recovery" ||
		task.ExecutionState == "running" && !task.LeaseActive
}

func secretaryTaskQuery(query domain.SecretaryTaskQuery) (domain.SecretaryTaskQuery, error) {
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 || query.Limit > 100 || !validSecretaryProgressCursor(query.Cursor) {
		return query, searchHTTPError(
			http.StatusBadRequest,
			"Invalid Secretary task cursor or limit",
		)
	}
	return query, nil
}

func validSecretaryTaskID(id string, allowEmpty bool) bool {
	if len(id) > 512 || !allowEmpty && id == "" {
		return false
	}
	for _, character := range id {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}

func validSecretaryTaskAction(id string, action domain.SecretaryTaskAction) bool {
	if !validSecretaryTaskID(id, false) || !validSecretaryTaskID(action.BatchID, true) ||
		!validSecretaryTaskID(action.AttemptID, true) ||
		action.ExpectedRevision < 0 ||
		action.SettingsRevision < 0 ||
		action.RetryEpoch < 0 {
		return false
	}
	switch action.Action {
	case "pause", "resume", "retry", "recheck":
		return true
	default:
		return false
	}
}

func secretaryTaskError(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.ErrNotFound
	case errors.Is(err, domain.ErrUnauthorized):
		return domain.ErrUnauthorized
	case errors.Is(err, domain.ErrInvalidCursor):
		return searchHTTPError(http.StatusBadRequest, "Invalid Secretary task cursor")
	case errors.Is(err, domain.ErrSourceVersionUnavailable),
		errors.Is(err, domain.ErrSummaryVersionMismatch):
		return searchHTTPError(
			http.StatusConflict,
			"The source or summary changed. Refresh before checking this result again.",
		)
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrSecretaryFenced):
		return searchHTTPError(
			http.StatusConflict,
			"The task changed or has an active execution. Refresh before trying again.",
		)
	default:
		return searchHTTPError(
			http.StatusInternalServerError,
			"Secretary task operation failed. Refresh before trying again.",
		)
	}
}

func safeSecretaryTask(task domain.SecretaryTask) domain.SecretaryTask {
	task.ErrorCode, task.ErrorMessage = safeSecretaryTaskFailure(task.ErrorCode)
	return task
}

func safeSecretaryTaskFailure(code string) (string, string) {
	switch code {
	case "model_connection_failed":
		return code, "The model connection failed. The previous result is unconfirmed; check the existing result or confirm a new attempt."
	case "execution_unknown":
		return code, "The previous execution result could not be confirmed."
	case "connection_backoff":
		return code, "Waiting to reconnect. Secretary will retry automatically."
	case "connection_retries_exhausted":
		return code, "Automatic connection retries are exhausted."
	case "invalid_output":
		return code, "The saved result failed validation."
	case "execution_failed":
		return code, "The last execution did not complete."
	case "retry_required":
		return code, "This task requires an explicit retry."
	case "source_incomplete":
		return code, "Waiting for the source turn to finish."
	default:
		return "", ""
	}
}

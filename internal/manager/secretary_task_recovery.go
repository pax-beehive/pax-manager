package manager

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func (s *Service) recheckSecretaryTask(
	ctx context.Context,
	owner, taskID string,
	action domain.SecretaryTaskAction,
) (string, error) {
	return recoverSecretaryTask(ctx, s.secretaryStore, s.secretaryExecutor(), owner, taskID, action)
}

func recoverSecretaryTask(
	ctx context.Context,
	store domain.SecretaryStore,
	executor secretary.Executor,
	owner, taskID string,
	action domain.SecretaryTaskAction,
) (string, error) {
	tasks, ok := store.(domain.SecretaryTaskStore)
	if !ok || tasks == nil {
		return "", apperr.Error{
			Status:  http.StatusServiceUnavailable,
			Message: "Secretary task controls are unavailable",
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	job, err := tasks.ClaimSecretaryTaskRecovery(ctx, owner, taskID, action, time.Minute)
	if err != nil {
		return "", err
	}
	runErr := secretary.NewWorker(store, executor, secretary.WorkerConfig{}).RecoverJob(ctx, job)
	releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer releaseCancel()
	if err = tasks.ReleaseSecretaryTaskRecovery(releaseCtx, job); err != nil {
		return "", errors.Join(runErr, err)
	}
	if runErr == nil {
		return "recovered", nil
	}
	if errors.Is(runErr, domain.ErrSecretaryExecutionUnknown) {
		return "still_unknown", nil
	}
	if errors.Is(runErr, domain.ErrSecretaryExecutionFailed) {
		return "result_failed", nil
	}
	if domain.SecretaryValidationFailureCode(runErr) != "" ||
		errors.Is(
			runErr,
			secretary.ErrNoOutput,
		) || errors.Is(runErr, domain.ErrSecretaryInvalidReadReceipt) {
		return "result_invalid", nil
	}
	return "", runErr
}

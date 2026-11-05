package secretary

import (
	"context"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// RecoverJob checks a specifically leased original attempt. It never prepares
// new input, begins another attempt, or invokes Executor.Execute.
func (w *Worker) RecoverJob(ctx context.Context, job domain.SecretaryJob) error {
	if !validNodeTaskRecovery(job) {
		return domain.ErrSecretaryFenced
	}
	executions, hasExecutions := w.store.(domain.SecretaryExecutionStore)
	nodes, hasNodes := w.store.(domain.SecretaryNodeStore)
	if !hasExecutions || !hasNodes {
		return domain.ErrSecretaryExecutionBlocked
	}
	if err := w.ensureActive(ctx, job); err != nil {
		return err
	}
	job.RunID = job.AttemptID
	result := ExecutionResult{Text: job.Attempt.Output, Reads: job.Attempt.Reads}
	if job.Attempt.State != "output_received" {
		var err error
		result, err = w.recoverNodeAttempt(ctx, job)
		if recoveryInputFailure(err) {
			return err
		}
		if err != nil && !completedOutputFailure(err) && !errors.Is(err, ErrNoOutput) &&
			!errors.Is(err, domain.ErrSecretaryExecutionUnknown) {
			err = errors.Join(domain.ErrSecretaryExecutionUnknown, err)
		}
		if err = w.persistRecoveredNodeOutput(ctx, executions, job, result, err); err != nil {
			return err
		}
	}
	if err := w.ensureActive(ctx, job); err != nil {
		return err
	}
	job, publication, err := buildNodeResult(job, result)
	if err != nil {
		if recoveryInputFailure(err) {
			return err
		}
		return w.failNodeAttempt(ctx, executions, job, err)
	}
	_, err = nodes.PublishSecretaryBatch(ctx, job, publication)
	if recoveryInputFailure(err) {
		return err
	}
	if errors.Is(err, domain.ErrConflict) {
		return w.failNodeAttempt(
			ctx,
			executions,
			job,
			domain.SecretaryOutputValidationError{Code: "schema", Err: err},
		)
	}
	return err
}

func (w *Worker) persistRecoveredNodeOutput(
	ctx context.Context,
	executions domain.SecretaryExecutionStore,
	job domain.SecretaryJob,
	result ExecutionResult,
	failure error,
) error {
	if errors.Is(failure, domain.ErrSecretaryExecutionUnknown) {
		return failure
	}
	if failure != nil && !completedOutputFailure(failure) {
		return w.failNodeAttempt(ctx, executions, job, failure)
	}
	if err := executions.SaveSecretaryAttemptOutput(ctx, job, job.AttemptID, result.Text, result.Reads); err != nil {
		return err
	}
	if failure != nil {
		return w.failNodeAttempt(ctx, executions, job, failure)
	}
	return nil
}

func recoveryInputFailure(err error) bool {
	return errors.Is(err, domain.ErrSourceVersionUnavailable) ||
		errors.Is(
			err,
			domain.ErrSummaryVersionMismatch,
		) || errors.Is(err, domain.ErrSecretaryFenced) ||
		errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnauthorized)
}

func validNodeTaskRecovery(job domain.SecretaryJob) bool {
	return job.BatchID != "" && job.AttemptID != "" && job.LeaseToken != "" &&
		job.Attempt != nil && job.Attempt.ID == job.AttemptID &&
		job.Attempt.BatchID == job.BatchID &&
		(job.Attempt.State == "unknown" || job.Attempt.State == "running" ||
			job.Attempt.State == "output_received")
}

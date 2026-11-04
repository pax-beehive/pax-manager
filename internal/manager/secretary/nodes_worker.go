package secretary

import (
	"context"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (w *Worker) processNodeJob(
	ctx context.Context,
	claimed domain.SecretaryJob,
	executions domain.SecretaryExecutionStore,
	nodes domain.SecretaryNodeStore,
) error {
	job, err := executions.PrepareSecretaryBatch(ctx, claimed)
	if errors.Is(err, domain.ErrSecretarySourceIncomplete) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = w.ensureActive(ctx, job); err != nil {
		return err
	}
	var result ExecutionResult
	if job.Attempt != nil && job.Attempt.State == "output_received" {
		job.AttemptID, job.RunID = job.Attempt.ID, job.Attempt.ID
		result.Text, result.Reads = job.Attempt.Output, job.Attempt.Reads
	} else {
		job, result, err = w.obtainNodeOutput(ctx, job, executions)
		if err != nil {
			return err
		}
	}
	if err = w.ensureActive(ctx, job); err != nil {
		return err
	}
	publication, err := BuildNodePublication(job, result.Text, result.Reads)
	if err != nil {
		return w.failNodeAttempt(ctx, executions, job, err)
	}
	for _, summary := range result.Reads.SummaryVersions {
		found := false
		for _, current := range job.CurrentSummaries {
			found = found || current.SummaryID == summary.SummaryID
		}
		if !found {
			job.CurrentSummaries = append(job.CurrentSummaries, summary)
		}
	}
	refs, err := RequestedNodeSourceReads(job, result.Text)
	if err != nil {
		return w.failNodeAttempt(ctx, executions, job, err)
	}
	job.AllowedRelatedMessages = refs
	job.AllowedRelatedSummaries = result.Reads.Summaries
	return w.publishNodeBatch(ctx, job, executions, nodes, publication)
}

func (w *Worker) publishNodeBatch(
	ctx context.Context,
	job domain.SecretaryJob,
	executions domain.SecretaryExecutionStore,
	nodes domain.SecretaryNodeStore,
	publication domain.SecretaryBatchPublication,
) error {
	_, err := nodes.PublishSecretaryBatch(ctx, job, publication)
	if errors.Is(err, domain.ErrSourceVersionUnavailable) ||
		errors.Is(err, domain.ErrSummaryVersionMismatch) {
		return w.failNodeAttempt(ctx, executions, job, err)
	}
	if errors.Is(err, domain.ErrSecretaryFenced) || errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrUnauthorized) {
		return w.failNodeAttempt(ctx, executions, job, errors.Join(domain.ErrSecretaryFenced, err))
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

func (w *Worker) obtainNodeOutput(
	ctx context.Context,
	job domain.SecretaryJob,
	executions domain.SecretaryExecutionStore,
) (domain.SecretaryJob, ExecutionResult, error) {
	if job.Attempt != nil && (job.Attempt.State == "running" || job.Attempt.State == "unknown") {
		job.AttemptID, job.RunID = job.Attempt.ID, job.Attempt.ID
		result, err := w.recoverNodeAttempt(ctx, job)
		return job, result, w.persistNodeOutput(ctx, executions, job, result, err)
	}
	prompt, err := BuildNodePrompt(job)
	if err != nil {
		return job, ExecutionResult{}, domain.SecretaryOutputValidationError{
			Code: "schema",
			Err:  err,
		}
	}
	if w.executor == nil {
		return job, ExecutionResult{}, errors.New("secretary agent executor is unavailable")
	}
	attempt, err := executions.BeginSecretaryAttempt(ctx, job)
	if err != nil {
		return job, ExecutionResult{}, err
	}
	job.Attempt, job.AttemptID, job.RunID = &attempt, attempt.ID, attempt.ID
	result, err := w.executeNodeAttempt(ctx, job, prompt)
	return job, result, w.persistNodeOutput(ctx, executions, job, result, err)
}

func (w *Worker) recoverNodeAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
) (ExecutionResult, error) {
	recovery, ok := w.executor.(RecoveryExecutor)
	if !ok {
		return ExecutionResult{}, domain.ErrSecretaryExecutionUnknown
	}
	result, completed, err := recovery.Recover(
		ctx,
		ExecutionRequest{
			Job:     job,
			OwnerID: job.OwnerUserID,
			AgentID: job.AgentID,
			RunID:   job.RunID,
		},
	)
	if err != nil {
		return result, err
	}
	if !completed {
		return ExecutionResult{}, domain.ErrSecretaryExecutionUnknown
	}
	return result, nil
}

func (w *Worker) persistNodeOutput(
	ctx context.Context,
	executions domain.SecretaryExecutionStore,
	job domain.SecretaryJob,
	result ExecutionResult,
	failure error,
) error {
	if failure != nil && !completedOutputFailure(failure) {
		return w.failNodeAttempt(ctx, executions, job, failure)
	}
	if err := executions.SaveSecretaryAttemptOutput(ctx, job, job.AttemptID, result.Text, result.Reads); err != nil {
		return w.failNodeAttempt(
			ctx,
			executions,
			job,
			errors.Join(domain.ErrSecretaryExecutionUnknown, err),
		)
	}
	if failure != nil {
		return w.failNodeAttempt(ctx, executions, job, failure)
	}
	return nil
}

func (w *Worker) executeNodeAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
	prompt string,
) (ExecutionResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, w.cfg.ExecutionTimeout)
	defer cancel()
	monitorErrors := make(chan error, 1)
	monitorDone := make(chan struct{})
	go w.monitorJob(runCtx, cancel, job, monitorErrors, monitorDone)
	defer func() { cancel(); <-monitorDone }()
	result, err := w.executor.Execute(runCtx, ExecutionRequest{
		Job: job, OwnerID: job.OwnerUserID, AgentID: job.AgentID, RunID: job.RunID, Prompt: prompt,
		BeforePrompt: func(promptCtx context.Context, agentID, sessionID string) error {
			if activeErr := w.ensureActive(promptCtx, job); activeErr != nil {
				return activeErr
			}
			return w.store.RegisterSecretaryInternalSession(promptCtx, job, agentID, sessionID)
		},
	})
	select {
	case monitorErr := <-monitorErrors:
		return result, monitorErr
	default:
	}
	return result, err
}

func (w *Worker) failNodeAttempt(
	ctx context.Context,
	executions domain.SecretaryExecutionStore,
	job domain.SecretaryJob,
	failure error,
) error {
	if !completedOutputFailure(failure) && !errors.Is(failure, ErrNoOutput) &&
		!errors.Is(failure, domain.ErrSecretaryFenced) &&
		!errors.Is(failure, domain.ErrSourceVersionUnavailable) &&
		!errors.Is(failure, domain.ErrSummaryVersionMismatch) {
		failure = errors.Join(domain.ErrSecretaryExecutionUnknown, failure)
	}
	failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := executions.FailSecretaryAttempt(failureCtx, job, job.AttemptID, failure); err != nil {
		return errors.Join(failure, err)
	}
	return failure
}

func completedOutputFailure(failure error) bool {
	var validation domain.SecretaryOutputValidationError
	return errors.As(failure, &validation) ||
		errors.Is(failure, domain.ErrSecretaryInvalidReadReceipt)
}

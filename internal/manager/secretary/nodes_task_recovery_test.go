package secretary

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func taskRecoveryJob(state string) domain.SecretaryJob {
	job := testJob()
	job.BatchID, job.AttemptID, job.RunID = "fixed-batch", "original-attempt", "original-attempt"
	job.Attempt = &domain.SecretaryAttempt{
		ID: job.AttemptID, BatchID: job.BatchID, State: state,
	}
	return job
}

func TestNodeTaskRecoveryGivenCompletedOriginalExecutionThenPublishesWithoutNewAttempt(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	job := taskRecoveryJob("unknown")
	output := nodeBatchOutput(t, []generatedNodeSummary{newNodeProposal()}, summarizedNodeOutcome())
	worker := NewWorker(store, nodeRecoveringExecutor{
		Executor: ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			t.Fatal("checking an original result must never execute a new model request")
			return ExecutionResult{}, nil
		}),
		recover: func(_ context.Context, request ExecutionRequest) (ExecutionResult, bool, error) {
			store.events = append(store.events, "recover")
			assert.Equal(t, job.AttemptID, request.RunID)
			assert.Equal(t, job.BatchID, request.Job.BatchID)
			assert.Empty(t, request.Prompt)
			return ExecutionResult{Text: output}, true, nil
		},
	}, WorkerConfig{})
	require.NoError(t, worker.RecoverJob(t.Context(), job))
	assert.Equal(t, []string{"recover", "save", "publish"}, store.events)
	assert.Equal(t, output, store.savedText)
	assert.Empty(t, store.failed)
}

func TestNodeTaskRecoveryGivenSavedOutputThenPublishesWithoutContactingExecutor(t *testing.T) {
	store := newNodeWorkerStore()
	job := taskRecoveryJob("output_received")
	job.Attempt.Output = nodeBatchOutput(
		t,
		[]generatedNodeSummary{newNodeProposal()},
		summarizedNodeOutcome(),
	)
	worker := NewWorker(store, nil, WorkerConfig{})
	require.NoError(t, worker.RecoverJob(t.Context(), job))
	assert.Equal(t, []string{"publish"}, store.events)
}

func TestNodeTaskRecoveryGivenVerifiedRelatedReadsThenPreservesPublicationAuthorization(
	t *testing.T,
) {
	job := nodeTestJob()
	baseline := job.CurrentSummaries[0]
	additional := baseline
	additional.SummaryID = "additional-summary"
	ref := domain.MessageVersionRef{
		MessageID: "related-message", ContentHash: domain.SearchContentHash("Related decision"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "proof",
	}
	summary := newNodeProposal()
	summary.RelatedSources = []domain.MessageVersionRef{ref}
	reads := domain.SecretaryToolReads{
		SummaryVersions: []domain.SummaryVersion{baseline, additional},
		Summaries: []domain.SummaryRef{
			{SummaryID: additional.SummaryID, Revision: additional.Revision},
		},
		Messages: []domain.SecretaryReadMessage{
			{Ref: ref, Text: "Related decision", View: "full", ReadReceipt: "proof"},
		},
	}
	updated, publication, err := buildNodeResult(job, ExecutionResult{
		Text: nodeBatchOutput(
			t,
			[]generatedNodeSummary{summary},
			summarizedNodeOutcome(),
		), Reads: reads,
	})
	require.NoError(t, err)
	assert.Equal(t, []domain.SummaryVersion{baseline, additional}, updated.CurrentSummaries)
	assert.Equal(t, []domain.MessageVersionRef{ref}, updated.AllowedRelatedMessages)
	assert.Equal(t, reads.Summaries, updated.AllowedRelatedSummaries)
	require.Len(t, publication.Outputs, 1)
	assert.Contains(t, publication.Outputs[0].Request.Coverage, ref)
	assert.Equal(t, []domain.SummaryVersion{baseline}, job.CurrentSummaries)
}

func TestNodeTaskRecoveryGivenUnconfirmedOutcomeThenKeepsOriginalExecutionUnknown(t *testing.T) {
	for _, failure := range []error{nil, errors.New("native completion unavailable"), domain.ErrSecretaryNotDispatched} {
		store := newNodeWorkerStore()
		worker := NewWorker(store, nodeRecoveringExecutor{
			Executor: ExecutorFunc(
				func(context.Context, ExecutionRequest) (ExecutionResult, error) {
					t.Fatal("checking cannot dispatch")
					return ExecutionResult{}, nil
				},
			),
			recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
				return ExecutionResult{}, false, failure
			},
		}, WorkerConfig{})
		err := worker.RecoverJob(t.Context(), taskRecoveryJob("unknown"))
		require.ErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
		assert.Empty(t, store.events)
		assert.Empty(
			t,
			store.attemptFailures,
			"an unsuccessful check must preserve the original execution failure",
		)
		assert.Empty(t, store.failed)
		assert.Empty(t, store.published)
	}
}

func TestNodeTaskRecoveryGivenMismatchedOrIneligibleAttemptThenRejectsWithoutReadingOrDispatching(
	t *testing.T,
) {
	for name, mutate := range map[string]func(*domain.SecretaryJob){
		"missing attempt":    func(job *domain.SecretaryJob) { job.Attempt = nil },
		"missing batch":      func(job *domain.SecretaryJob) { job.BatchID = "" },
		"missing attempt id": func(job *domain.SecretaryJob) { job.AttemptID = "" },
		"wrong attempt":      func(job *domain.SecretaryJob) { job.Attempt.ID = "different" },
		"wrong batch":        func(job *domain.SecretaryJob) { job.Attempt.BatchID = "different" },
		"failed attempt":     func(job *domain.SecretaryJob) { job.Attempt.State = "failed" },
		"missing lease":      func(job *domain.SecretaryJob) { job.LeaseToken = "" },
	} {
		t.Run(name, func(t *testing.T) {
			store := newNodeWorkerStore()
			job := taskRecoveryJob("unknown")
			mutate(&job)
			worker := NewWorker(store, nil, WorkerConfig{})
			assert.ErrorIs(t, worker.RecoverJob(t.Context(), job), domain.ErrSecretaryFenced)
			assert.Empty(t, store.events)
		})
	}
}

func TestNodeTaskRecoveryGivenCanceledOrLostLeaseThenNeverPublishes(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		store := newNodeWorkerStore()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if canceled {
			cancel()
		} else {
			store.active.Store(false)
		}
		worker := NewWorker(store, nil, WorkerConfig{})
		err := worker.RecoverJob(ctx, taskRecoveryJob("unknown"))
		if canceled {
			assert.ErrorIs(t, err, context.Canceled)
		} else {
			assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
		}
		assert.Empty(t, store.events)
	}
}

func TestNodeTaskRecoveryGivenNoRecoveryContractThenCannotExecuteFreshWork(t *testing.T) {
	store := newNodeWorkerStore()
	worker := NewWorker(store, nil, WorkerConfig{})
	assert.ErrorIs(
		t,
		worker.RecoverJob(t.Context(), taskRecoveryJob("unknown")),
		domain.ErrSecretaryExecutionUnknown,
	)
	assert.Empty(t, store.events)
	assert.Empty(t, store.published)
	unsupported := NewWorker(newWorkerStore(), nil, WorkerConfig{})
	assert.ErrorIs(
		t,
		unsupported.RecoverJob(t.Context(), taskRecoveryJob("unknown")),
		domain.ErrSecretaryExecutionBlocked,
	)
}

func TestNodeTaskRecoveryGivenInvalidCompletedOutputThenPreservesOutputAndReportsInvalid(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	worker := NewWorker(store, nodeRecoveringExecutor{
		recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
			return ExecutionResult{Text: "not valid summary JSON"}, true, nil
		},
	}, WorkerConfig{})
	err := worker.RecoverJob(t.Context(), taskRecoveryJob("unknown"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
	assert.Equal(t, []string{"save", "fail-attempt"}, store.events)
	assert.Equal(t, "not valid summary JSON", store.savedText)
	assert.Empty(t, store.published)
}

func TestNodeTaskRecoveryGivenChangedBaselineThenDoesNotSupersedeOrScheduleFreshInput(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	job := taskRecoveryJob("output_received")
	job.Attempt.Output = nodeBatchOutput(
		t,
		nil,
		[]generatedNodeOutcome{{MessageID: "message-1", Action: "no_summary", Reason: "Greeting"}},
	)
	job.CurrentSummaries = []domain.SummaryVersion{
		{SummaryID: "same-head", OwnerUserID: job.OwnerUserID, Revision: 1},
		{SummaryID: "same-head", OwnerUserID: job.OwnerUserID, Revision: 2},
	}
	err := NewWorker(store, nil, WorkerConfig{}).RecoverJob(t.Context(), job)
	require.ErrorIs(t, err, domain.ErrSummaryVersionMismatch)
	assert.Empty(
		t,
		store.events,
		"a read-only check cannot invoke failure handling that changes the retry epoch",
	)
	assert.Empty(t, store.attemptFailures)
}

func TestNodeTaskRecoveryGivenPublicationRaceThenDoesNotReplanTheTask(t *testing.T) {
	for _, failure := range []error{domain.ErrSourceVersionUnavailable, domain.ErrSummaryVersionMismatch,
		domain.ErrSecretaryFenced, domain.ErrNotFound, domain.ErrUnauthorized} {
		store := newNodeWorkerStore()
		store.publishErr = failure
		job := taskRecoveryJob("output_received")
		job.Attempt.Output = nodeBatchOutput(
			t,
			[]generatedNodeSummary{newNodeProposal()},
			summarizedNodeOutcome(),
		)
		err := NewWorker(store, nil, WorkerConfig{}).RecoverJob(t.Context(), job)
		assert.ErrorIs(t, err, failure)
		assert.Equal(t, []string{"publish"}, store.events)
		assert.Empty(t, store.attemptFailures)
	}
}

func TestNodeTaskRecoveryGivenUnavailableOriginalOrStorageThenDoesNotRewriteTheAttempt(
	t *testing.T,
) {
	for _, phase := range []string{"read", "save"} {
		store := newNodeWorkerStore()
		failure := domain.ErrSourceVersionUnavailable
		if phase == "save" {
			store.saveErr = failure
		}
		output := nodeBatchOutput(
			t,
			[]generatedNodeSummary{newNodeProposal()},
			summarizedNodeOutcome(),
		)
		worker := NewWorker(store, nodeRecoveringExecutor{
			recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
				if phase == "read" {
					return ExecutionResult{}, false, failure
				}
				return ExecutionResult{Text: output}, true, nil
			},
		}, WorkerConfig{})
		assert.ErrorIs(t, worker.RecoverJob(t.Context(), taskRecoveryJob("unknown")), failure)
		assert.Empty(t, store.attemptFailures)
		assert.Empty(t, store.published)
	}
}

func TestNodeTaskRecoveryGivenCompletedButRejectedResultThenRecordsItsValidationFailure(
	t *testing.T,
) {
	for _, failure := range []error{
		domain.SecretaryOutputValidationError{Code: "schema", Err: errors.New("invalid result")},
		domain.ErrSecretaryInvalidReadReceipt,
		ErrNoOutput,
	} {
		store := newNodeWorkerStore()
		worker := NewWorker(store, nodeRecoveringExecutor{
			recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
				return ExecutionResult{Text: "completed response"}, true, failure
			},
		}, WorkerConfig{})
		err := worker.RecoverJob(t.Context(), taskRecoveryJob("unknown"))
		assert.ErrorIs(t, err, failure)
		assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
		if errors.Is(failure, ErrNoOutput) {
			assert.Equal(t, []string{"fail-attempt"}, store.events)
		} else {
			assert.Equal(t, []string{"save", "fail-attempt"}, store.events)
		}
		assert.Empty(t, store.published)
	}
}

func TestNodeTaskRecoveryGivenConflictingPublicationThenReportsInvalidWithoutReexecuting(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	store.publishErr = domain.ErrConflict
	job := taskRecoveryJob("output_received")
	job.Attempt.Output = nodeBatchOutput(
		t,
		[]generatedNodeSummary{newNodeProposal()},
		summarizedNodeOutcome(),
	)
	err := NewWorker(store, nil, WorkerConfig{}).RecoverJob(t.Context(), job)
	assert.ErrorIs(t, err, domain.ErrConflict)
	assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
	assert.Equal(t, []string{"publish", "fail-attempt"}, store.events)
}

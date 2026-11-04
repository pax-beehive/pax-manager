package secretary

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type nodeWorkerStore struct {
	*workerStore
	prepareErr      error
	beginErr        error
	saveErr         error
	failAttemptErr  error
	stored          *domain.SecretaryAttempt
	events          []string
	attemptFailures []error
	savedText       string
	published       []domain.SecretaryBatchPublication
	prepared        domain.SecretaryJob
}

func newNodeWorkerStore() *nodeWorkerStore {
	return &nodeWorkerStore{workerStore: newWorkerStore()}
}

func (s *nodeWorkerStore) PrepareSecretaryBatch(
	_ context.Context,
	job domain.SecretaryJob,
) (domain.SecretaryJob, error) {
	s.events = append(s.events, "prepare")
	job.BatchID = "fixed-batch"
	job.Attempt = s.stored
	s.prepared = job
	return job, s.prepareErr
}

func (s *nodeWorkerStore) BeginSecretaryAttempt(
	context.Context,
	domain.SecretaryJob,
) (domain.SecretaryAttempt, error) {
	s.events = append(s.events, "begin")
	return domain.SecretaryAttempt{
		ID:      "attempt-one",
		BatchID: "fixed-batch",
		State:   "running",
	}, s.beginErr
}

func (s *nodeWorkerStore) SaveSecretaryAttemptOutput(
	_ context.Context,
	job domain.SecretaryJob,
	id, output string,
	reads domain.SecretaryToolReads,
) error {
	s.events = append(s.events, "save")
	s.savedText = output
	if id != job.AttemptID {
		return domain.ErrSecretaryFenced
	}
	if s.saveErr == nil {
		s.stored = &domain.SecretaryAttempt{
			ID:      id,
			BatchID: job.BatchID,
			State:   "output_received",
			Output:  output,
			Reads:   reads,
		}
	}
	return s.saveErr
}

type nodeRecoveringExecutor struct {
	Executor
	recover func(context.Context, ExecutionRequest) (ExecutionResult, bool, error)
}

func (e nodeRecoveringExecutor) Recover(
	ctx context.Context,
	request ExecutionRequest,
) (ExecutionResult, bool, error) {
	return e.recover(ctx, request)
}

func TestNodeWorkerGivenPersistedNativeCompletionThenRecoversReadOnlyAndCommits(t *testing.T) {
	store := newNodeWorkerStore()
	store.stored = &domain.SecretaryAttempt{
		ID:      "persisted",
		BatchID: "fixed-batch",
		State:   "running",
	}
	output := nodeBatchOutput(t, []generatedNodeSummary{newNodeProposal()}, summarizedNodeOutcome())
	worker := NewWorker(
		store,
		nodeRecoveringExecutor{
			recover: func(_ context.Context, request ExecutionRequest) (ExecutionResult, bool, error) {
				store.events = append(store.events, "recover")
				assert.Equal(t, "persisted", request.RunID)
				assert.Empty(t, request.Prompt)
				return ExecutionResult{Text: output}, true, nil
			},
		},
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, []string{"prepare", "recover", "save", "publish"}, store.events)
	assert.Equal(t, output, store.savedText)
}

func TestNodeWorkerGivenNativeOutcomeStillUnknownThenDoesNotBeginFreshAttempt(t *testing.T) {
	for _, recoverable := range []bool{false, true} {
		store := newNodeWorkerStore()
		store.stored = &domain.SecretaryAttempt{
			ID:      "persisted",
			BatchID: "fixed-batch",
			State:   "unknown",
		}
		var executor Executor = ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			t.Fatal("unknown attempts cannot send another prompt")
			return ExecutionResult{}, nil
		})
		if recoverable {
			executor = nodeRecoveringExecutor{
				Executor: executor,
				recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
					return ExecutionResult{}, false, nil
				},
			}
		}
		worker := NewWorker(store, executor, WorkerConfig{})
		assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryExecutionUnknown)
		assert.Equal(t, []string{"prepare", "fail-attempt"}, store.events)
	}
}

func TestNodeWorkerGivenTemporaryCommitFailureAfterSavingThenRetriesSavedOutputWithoutModel(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	store.publishErr = errors.New("database temporarily unavailable")
	failure := store.publishErr
	calls := 0
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			calls++
			return ExecutionResult{
				Text: nodeBatchOutput(
					t,
					[]generatedNodeSummary{newNodeProposal()},
					summarizedNodeOutcome(),
				),
			}, nil
		}),
		WorkerConfig{},
	)
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), failure)
	store.jobs, store.publishErr = []domain.SecretaryJob{testJob()}, nil
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, 1, calls)
	assert.Len(t, store.published, 2)
}

func TestNodeWorkerGivenPermanentPublishFailureThenDoesNotReplayTheSameInvalidOutput(t *testing.T) {
	for name, failure := range map[string]error{
		"schema":          domain.ErrConflict,
		"source changed":  domain.ErrSourceVersionUnavailable,
		"summary changed": domain.ErrSummaryVersionMismatch,
		"revoked":         domain.ErrNotFound,
		"fenced":          domain.ErrSecretaryFenced,
	} {
		t.Run(name, func(t *testing.T) {
			store := newNodeWorkerStore()
			store.publishErr = failure
			worker := NewWorker(
				store,
				ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
					return ExecutionResult{
						Text: nodeBatchOutput(
							t,
							[]generatedNodeSummary{newNodeProposal()},
							summarizedNodeOutcome(),
						),
					}, nil
				}),
				WorkerConfig{},
			)
			err := worker.ProcessOnce(t.Context())
			assert.ErrorIs(t, err, failure)
			assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
			assert.Len(t, store.attemptFailures, 1)
		})
	}
}

func TestNodeWorkerGivenIncompleteSourceThenWaitsWithoutReportingFailure(t *testing.T) {
	store := newNodeWorkerStore()
	store.prepareErr = domain.ErrSecretarySourceIncomplete
	worker := NewWorker(store, nil, WorkerConfig{})
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Empty(t, store.failed)
	assert.Equal(t, []string{"prepare"}, store.events)
}

func (s *nodeWorkerStore) FailSecretaryAttempt(
	_ context.Context,
	_ domain.SecretaryJob,
	_ string,
	failure error,
) error {
	s.events = append(s.events, "fail-attempt")
	s.attemptFailures = append(s.attemptFailures, failure)
	return s.failAttemptErr
}

func (s *nodeWorkerStore) PublishSecretaryBatch(
	_ context.Context,
	job domain.SecretaryJob,
	publication domain.SecretaryBatchPublication,
) ([]domain.SummaryVersion, error) {
	s.events = append(s.events, "publish")
	s.published = append(s.published, publication)
	s.publishedJobs = append(s.publishedJobs, job)
	return nil, s.publishErr
}

func (*nodeWorkerStore) ListSecretaryNodeSummaries(
	context.Context,
	string,
	string,
	domain.SummaryListQuery,
) (domain.SecretarySummaryPage, error) {
	return domain.SecretarySummaryPage{}, nil
}

func TestNodeWorkerGivenFreshBatchThenPersistsOutputBeforeAtomicPublication(t *testing.T) {
	store := newNodeWorkerStore()
	output := nodeBatchOutput(t, []generatedNodeSummary{newNodeProposal()}, summarizedNodeOutcome())
	worker := NewWorker(
		store,
		ExecutorFunc(func(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
			store.events = append(store.events, "execute")
			assert.Equal(t, "attempt-one", request.RunID)
			assert.Equal(t, "attempt-one", request.Job.AttemptID)
			assert.Contains(t, request.Prompt, "independent summary nodes")
			require.NoError(t, request.BeforePrompt(ctx, request.AgentID, "internal-session"))
			return ExecutionResult{Text: output}, nil
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, []string{"prepare", "begin", "execute", "save", "publish"}, store.events)
	assert.Equal(t, output, store.savedText)
	assert.Len(t, store.published, 1)
	assert.Equal(t, 1, store.registered)
}

func TestNodeWorkerGivenSavedOutputAfterRestartThenCommitsWithoutCallingModel(t *testing.T) {
	store := newNodeWorkerStore()
	store.stored = &domain.SecretaryAttempt{
		ID:    "persisted",
		State: "output_received",
		Output: nodeBatchOutput(
			t,
			nil,
			[]generatedNodeOutcome{
				{MessageID: "message-1", Action: "no_summary", Reason: "Already known"},
			},
		),
	}
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			t.Fatal("stored output must never invoke the model again")
			return ExecutionResult{}, nil
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, []string{"prepare", "publish"}, store.events)
	assert.Equal(t, "persisted", store.publishedJobs[0].AttemptID)
}

func TestNodeWorkerGivenUnknownExistingExecutionThenDoesNotSendAnotherPrompt(t *testing.T) {
	store := newNodeWorkerStore()
	store.beginErr = domain.ErrSecretaryExecutionUnknown
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			t.Fatal("an unknown native execution cannot be replayed")
			return ExecutionResult{}, nil
		}),
		WorkerConfig{},
	)
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryExecutionUnknown)
	assert.Equal(t, []string{"prepare", "begin"}, store.events)
	assert.Empty(t, store.published)
}

func TestNodeWorkerGivenInvalidFinalOutputThenSavesItAndMarksAttemptInvalid(t *testing.T) {
	for _, nativeFailure := range []error{nil, generatedOutputError("schema", "malformed final JSON")} {
		store := newNodeWorkerStore()
		worker := NewWorker(
			store,
			ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
				return ExecutionResult{Text: "invalid final JSON"}, nativeFailure
			}),
			WorkerConfig{},
		)
		err := worker.ProcessOnce(t.Context())
		require.Error(t, err)
		assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
		assert.Equal(t, "invalid final JSON", store.savedText)
		assert.Equal(t, []string{"prepare", "begin", "save", "fail-attempt"}, store.events)
		assert.Empty(t, store.published)
	}
}

func TestNodeWorkerGivenAmbiguousNativeFailureThenStopsWithoutSavingPartialOutput(t *testing.T) {
	for name, failure := range map[string]error{
		"incomplete": ErrExecutionIncomplete,
		"approval":   ErrApprovalRequired,
		"timeout":    context.DeadlineExceeded,
		"transport":  errors.New("connection closed after sending"),
	} {
		t.Run(name, func(t *testing.T) {
			store := newNodeWorkerStore()
			worker := NewWorker(
				store,
				ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
					return ExecutionResult{Text: "partial"}, failure
				}),
				WorkerConfig{},
			)
			assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryExecutionUnknown)
			assert.Empty(t, store.savedText)
			assert.Empty(t, store.published)
			require.Len(t, store.attemptFailures, 1)
			assert.ErrorIs(t, store.attemptFailures[0], failure)
		})
	}
}

func TestNodeWorkerGivenCompletedOutputCannotBeSavedThenStopsAsUnknown(t *testing.T) {
	store := newNodeWorkerStore()
	store.saveErr = errors.New("database unavailable")
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			return ExecutionResult{
				Text: nodeBatchOutput(
					t,
					[]generatedNodeSummary{newNodeProposal()},
					summarizedNodeOutcome(),
				),
			}, nil
		}),
		WorkerConfig{},
	)
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryExecutionUnknown)
	assert.Empty(t, store.published)
}

func TestNodeWorkerGivenSetupOrCommitFailureThenNeverAdvancesProgress(t *testing.T) {
	for name, configure := range map[string]func(*nodeWorkerStore){
		"prepare":       func(s *nodeWorkerStore) { s.prepareErr = errors.New("prepare failed") },
		"fenced":        func(s *nodeWorkerStore) { s.active.Store(false) },
		"bad input":     func(s *nodeWorkerStore) { s.jobs[0].Messages = nil },
		"commit":        func(s *nodeWorkerStore) { s.publishErr = domain.ErrSummaryVersionMismatch },
		"failure write": func(s *nodeWorkerStore) { s.failAttemptErr = errors.New("failure write failed") },
	} {
		t.Run(name, func(t *testing.T) {
			store := newNodeWorkerStore()
			configure(store)
			worker := NewWorker(
				store,
				ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
					if name == "failure write" {
						return ExecutionResult{}, ErrNoOutput
					}
					return ExecutionResult{
						Text: nodeBatchOutput(
							t,
							[]generatedNodeSummary{newNodeProposal()},
							summarizedNodeOutcome(),
						),
					}, nil
				}),
				WorkerConfig{},
			)
			assert.Error(t, worker.ProcessOnce(t.Context()))
		})
	}
}

func TestNodeWorkerGivenLeaseRevokedDuringExecutionThenRejectsLateOutput(t *testing.T) {
	store := newNodeWorkerStore()
	worker := NewWorker(
		store,
		ExecutorFunc(func(ctx context.Context, _ ExecutionRequest) (ExecutionResult, error) {
			store.active.Store(false)
			<-ctx.Done()
			return ExecutionResult{Text: "late"}, ctx.Err()
		}),
		WorkerConfig{ActiveCheckInterval: time.Millisecond},
	)
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryFenced)
	assert.Empty(t, store.savedText)
	assert.Empty(t, store.published)
}

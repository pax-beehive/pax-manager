package secretary

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type workerStore struct {
	domain.SecretaryStore
	jobs          []domain.SecretaryJob
	claimErr      error
	backfillErr   error
	publishErr    error
	failErr       error
	activeErr     error
	active        atomic.Bool
	mu            sync.Mutex
	failed        []error
	publications  []domain.PublishSummaryRequest
	publishedJobs []domain.SecretaryJob
	registered    int
	backfilled    int
}

func newWorkerStore() *workerStore {
	store := &workerStore{jobs: []domain.SecretaryJob{testJob()}}
	store.active.Store(true)
	return store
}

func (s *workerStore) BackfillSecretaryJobs(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backfilled++
	return s.backfillErr
}

func (s *workerStore) ClaimSecretaryJobs(
	context.Context,
	int,
	time.Duration,
	time.Duration,
) ([]domain.SecretaryJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := s.jobs
	s.jobs = nil
	return jobs, s.claimErr
}

func (s *workerStore) RegisterSecretaryInternalSession(
	context.Context,
	domain.SecretaryJob,
	string,
	string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered++
	return nil
}
func (s *workerStore) SecretaryJobActive(context.Context, domain.SecretaryJob) (bool, error) {
	return s.active.Load(), s.activeErr
}

func (s *workerStore) PublishSecretaryJob(
	_ context.Context,
	job domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publications = append(s.publications, request)
	s.publishedJobs = append(s.publishedJobs, job)
	return domain.SummaryVersion{
		SummaryID: request.SummaryID,
		Revision:  request.ExpectedRevision + 1,
	}, s.publishErr
}
func (s *workerStore) FailSecretaryJob(_ context.Context, _ domain.SecretaryJob, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, err)
	return s.failErr
}

func TestWorkerGivenOwnedSourcesThenRunsSelectedAgentAndPublishesFixedCoverage(t *testing.T) {
	store := newWorkerStore()
	job := store.jobs[0]
	worker := NewWorker(
		store,
		ExecutorFunc(func(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
			assert.Equal(t, job.OwnerUserID, request.OwnerID)
			assert.Equal(t, job.AgentID, request.AgentID)
			assert.Contains(t, request.Prompt, job.Messages[0].Body)
			require.NoError(t, request.BeforePrompt(ctx, request.AgentID, "internal-session"))
			return ExecutionResult{
				SessionID: "internal-session",
				Text:      `{"title":"Progress","body":"Use the database."}`,
			}, nil
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	require.Len(t, store.publications, 1)
	assert.Len(t, store.publications[0].Coverage, len(job.Messages))
	assert.Empty(t, store.failed)
	assert.Equal(t, 1, store.registered)
}

func TestWorkerGivenFailureThenRetainsDurableWorkWithoutPublishing(t *testing.T) {
	for name, configure := range map[string]func(*workerStore) string{
		"bad output":          func(*workerStore) string { return "not json" },
		"inactive":            func(s *workerStore) string { s.active.Store(false); return `{}` },
		"active lookup fails": func(s *workerStore) string { s.activeErr = errors.New("storage unavailable"); return `{}` },
		"source too large":    func(s *workerStore) string { s.jobs[0].Messages = nil; return `{}` },
	} {
		t.Run(name, func(t *testing.T) {
			store := newWorkerStore()
			output := configure(store)
			worker := NewWorker(
				store,
				ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
					return ExecutionResult{Text: output}, nil
				}),
				WorkerConfig{},
			)
			assert.Error(t, worker.ProcessOnce(t.Context()))
			assert.Empty(t, store.publications)
			assert.Len(t, store.failed, 1)
		})
	}
}

func TestWorkerGivenSettingsRevokedDuringExecutionThenCancelsNativeTaskAndNeverPublishes(
	t *testing.T,
) {
	store := newWorkerStore()
	entered := make(chan struct{})
	worker := NewWorker(
		store,
		ExecutorFunc(func(ctx context.Context, _ ExecutionRequest) (ExecutionResult, error) {
			close(entered)
			<-ctx.Done()
			return ExecutionResult{}, ctx.Err()
		}),
		WorkerConfig{ActiveCheckInterval: time.Millisecond},
	)
	done := make(chan error, 1)
	go func() { done <- worker.ProcessOnce(t.Context()) }()
	<-entered
	store.active.Store(false)
	select {
	case err := <-done:
		assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel revoked work")
	}
	assert.Empty(t, store.publications)
}

func TestWorkerGivenAgentCompletesAfterRevocationThenRejectsPublication(t *testing.T) {
	store := newWorkerStore()
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			store.active.Store(false)
			return ExecutionResult{Text: `{"body":"changed"}`}, nil
		}),
		WorkerConfig{},
	)
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryFenced)
	assert.Empty(t, store.publications)
}

func TestWorkerGivenExecutionTimeoutAndPublishFailureThenAcknowledgesFailures(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		store := newWorkerStore()
		worker := NewWorker(
			store,
			ExecutorFunc(func(ctx context.Context, _ ExecutionRequest) (ExecutionResult, error) {
				<-ctx.Done()
				return ExecutionResult{}, ctx.Err()
			}),
			WorkerConfig{ExecutionTimeout: time.Millisecond},
		)
		assert.ErrorIs(t, worker.ProcessOnce(t.Context()), context.DeadlineExceeded)
		assert.Len(t, store.failed, 1)
	})
	t.Run("publication and failure persistence", func(t *testing.T) {
		store := newWorkerStore()
		store.publishErr = errors.New("publication failed")
		store.failErr = errors.New("failure persistence failed")
		worker := NewWorker(
			store,
			ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
				return ExecutionResult{Text: `{"body":"summary"}`}, nil
			}),
			WorkerConfig{},
		)
		err := worker.ProcessOnce(t.Context())
		assert.ErrorIs(t, err, store.publishErr)
		assert.ErrorIs(t, err, store.failErr)
	})
}

func TestWorkerGivenCancellationOrClaimFailureThenDoesNotDispatch(t *testing.T) {
	store := newWorkerStore()
	store.claimErr = errors.New("claim failed")
	worker := NewWorker(store, nil, WorkerConfig{})
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), store.claimErr)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, worker.ProcessOnce(ctx), context.Canceled)
}

func TestWorkerRunGivenBackfillFailureThenRetriesUntilCanceled(t *testing.T) {
	store := newWorkerStore()
	store.backfillErr = errors.New("backfill failed")
	ctx, cancel := context.WithCancel(t.Context())
	worker := NewWorker(
		store,
		nil,
		WorkerConfig{
			RetryInterval: time.Millisecond,
			OnError:       func(err error) { assert.ErrorIs(t, err, store.backfillErr); cancel() },
		},
	)
	assert.ErrorIs(t, worker.Run(ctx), context.Canceled)
	assert.Equal(t, 1, store.backfilled)
}

func TestWorkerRunGivenSuccessfulInitializationThenPollsAndStops(t *testing.T) {
	store := newWorkerStore()
	store.jobs = nil
	ctx, cancel := context.WithCancel(t.Context())
	worker := NewWorker(store, nil, WorkerConfig{PollInterval: time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(
		t,
		func() bool { store.mu.Lock(); defer store.mu.Unlock(); return store.backfilled == 1 },
		time.Second,
		time.Millisecond,
	)
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

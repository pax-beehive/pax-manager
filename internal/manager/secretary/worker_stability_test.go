package secretary

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// flakyFenceStore fails the fence lookup on demand without reporting a fence.
type flakyFenceStore struct {
	*nodeWorkerStore
	failing atomic.Bool
	checks  atomic.Int32
}

func (s *flakyFenceStore) SecretaryJobActive(
	ctx context.Context,
	job domain.SecretaryJob,
) (bool, error) {
	if s.failing.Load() {
		s.checks.Add(1)
		return false, errors.New("storage unavailable")
	}
	return s.nodeWorkerStore.SecretaryJobActive(ctx, job)
}

func TestNodeWorkerGivenFenceCheckFailsDuringExecutionThenKeepsRunningAndCommits(t *testing.T) {
	store := &flakyFenceStore{nodeWorkerStore: newNodeWorkerStore()}
	output := nodeBatchOutput(t, []generatedNodeSummary{newNodeProposal()}, summarizedNodeOutcome())
	var reported atomic.Int32
	worker := NewWorker(
		store,
		ExecutorFunc(func(ctx context.Context, _ ExecutionRequest) (ExecutionResult, error) {
			store.failing.Store(true)
			require.Eventually(t, func() bool { return store.checks.Load() >= 3 },
				time.Second, time.Millisecond, "the monitor must keep checking")
			store.failing.Store(false)
			if err := ctx.Err(); err != nil {
				return ExecutionResult{}, err
			}
			return ExecutionResult{Text: output}, nil
		}),
		WorkerConfig{
			ActiveCheckInterval: time.Millisecond,
			OnError:             func(error) { reported.Add(1) },
		},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()),
		"a storage hiccup must not turn a healthy execution into an unknown outcome")
	assert.Equal(t, output, store.savedText)
	assert.Len(t, store.published, 1)
	assert.Empty(t, store.attemptFailures)
	assert.Equal(t, int32(1), reported.Load(), "a flapping check is reported once per execution")
}

type restartingWorkerStore struct {
	*workerStore
	restarted []domain.SecretaryTaskRestart
	err       error
	calls     atomic.Int32
}

func (s *restartingWorkerStore) RestartStalledSecretaryTasks(
	context.Context,
	int,
) ([]domain.SecretaryTaskRestart, error) {
	s.calls.Add(1)
	return s.restarted, s.err
}

func TestWorkerGivenStalledTasksThenReportsEachAutomaticRestart(t *testing.T) {
	store := &restartingWorkerStore{workerStore: newWorkerStore()}
	store.restarted = []domain.SecretaryTaskRestart{
		{OwnerUserID: "owner", TaskID: "1", PreviousState: "unknown", Restarts: 1},
		{OwnerUserID: "owner", TaskID: "2", PreviousState: "blocked", Restarts: 2},
	}
	store.err = errors.New("one task could not be restarted")
	var reported []domain.SecretaryTaskRestart
	worker := NewWorker(store, nil, WorkerConfig{
		OnRestart: func(task domain.SecretaryTaskRestart) { reported = append(reported, task) },
	})
	// Partial progress is still reported alongside the failure.
	assert.ErrorIs(t, worker.RestartStalled(t.Context()), store.err)
	assert.Equal(t, store.restarted, reported)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, worker.RestartStalled(ctx), context.Canceled)
	assert.Equal(t, int32(1), store.calls.Load(), "a canceled worker must not touch stalled tasks")
}

func TestWorkerGivenStoreWithoutAutomaticRestartThenSkipsIt(t *testing.T) {
	worker := NewWorker(newWorkerStore(), nil, WorkerConfig{})
	assert.NoError(t, worker.RestartStalled(t.Context()))
}

func TestWorkerRunGivenStalledTasksThenChecksThemPeriodicallyAndSurfacesFailures(t *testing.T) {
	store := &restartingWorkerStore{workerStore: newWorkerStore()}
	store.jobs = nil
	store.err = errors.New("restart scan failed")
	ctx, cancel := context.WithCancel(t.Context())
	var failures atomic.Int32
	worker := NewWorker(store, nil, WorkerConfig{
		PollInterval:        time.Millisecond,
		RetryInterval:       time.Millisecond,
		AutoRestartInterval: time.Millisecond,
		OnError: func(err error) {
			// Shutdown can surface a bare cancellation; only scan failures count.
			if errors.Is(err, store.err) {
				failures.Add(1)
			}
		},
	})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool { return store.calls.Load() >= 2 && failures.Load() >= 2 },
		time.Second, time.Millisecond)
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestWorkerRunGivenBackfillNotFinishedThenDoesNotRestartStalledTasks(t *testing.T) {
	store := &restartingWorkerStore{workerStore: newWorkerStore()}
	store.backfillErr = errors.New("backfill failed")
	ctx, cancel := context.WithCancel(t.Context())
	var failures atomic.Int32
	worker := NewWorker(store, nil, WorkerConfig{
		RetryInterval:       time.Millisecond,
		AutoRestartInterval: time.Millisecond,
		OnError: func(error) {
			if failures.Add(1) >= 3 {
				cancel()
			}
		},
	})
	assert.ErrorIs(t, worker.Run(ctx), context.Canceled)
	assert.Zero(t, store.calls.Load())
}

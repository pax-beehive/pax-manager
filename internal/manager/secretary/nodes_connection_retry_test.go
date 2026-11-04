package secretary

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeWorkerGivenUndispatchedConnectionFailureThenLetsStoreScheduleRetry(t *testing.T) {
	store := newNodeWorkerStore()
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			store.events = append(store.events, "execute")
			return ExecutionResult{}, domain.ErrSecretaryNotDispatched
		}),
		WorkerConfig{},
	)
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, []string{"prepare", "begin", "execute", "fail-attempt"}, store.events)
	require.Len(t, store.attemptFailures, 1)
	assert.ErrorIs(t, store.attemptFailures[0], domain.ErrSecretaryNotDispatched)
	assert.NotErrorIs(t, store.attemptFailures[0], domain.ErrSecretaryExecutionUnknown)
	assert.Empty(
		t,
		store.failed,
		"the generic failure path must not replace the persisted retry deadline",
	)
	assert.Empty(t, store.published)
}

func TestNodeWorkerGivenRetryScheduleCannotPersistThenReportsFailure(t *testing.T) {
	store := newNodeWorkerStore()
	store.failAttemptErr = errors.New("retry state database failure")
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			return ExecutionResult{}, domain.ErrSecretaryNotDispatched
		}),
		WorkerConfig{},
	)
	err := worker.ProcessOnce(t.Context())
	assert.ErrorIs(t, err, store.failAttemptErr)
	assert.Len(t, store.failed, 1)
	assert.Empty(t, store.published)
}

func TestNodeWorkerGivenUncertainDispatchThenDoesNotScheduleFreshExecution(t *testing.T) {
	store := newNodeWorkerStore()
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			return ExecutionResult{}, errors.Join(
				domain.ErrSecretaryNotDispatched,
				domain.ErrSecretaryExecutionUnknown,
			)
		}),
		WorkerConfig{},
	)
	assert.ErrorIs(t, worker.ProcessOnce(t.Context()), domain.ErrSecretaryExecutionUnknown)
	assert.Len(t, store.failed, 1)
	assert.Empty(t, store.published)
}

func TestNodeWorkerGivenRetryDeadlineNotDueThenWaitsWithoutReportingFailure(t *testing.T) {
	for _, phase := range []string{"prepare", "begin"} {
		t.Run(phase, func(t *testing.T) {
			store := newNodeWorkerStore()
			if phase == "prepare" {
				store.prepareErr = domain.ErrSecretaryRetryDeferred
			} else {
				store.beginErr = domain.ErrSecretaryRetryDeferred
			}
			worker := NewWorker(
				store,
				ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
					t.Fatal("the persisted retry deadline has not arrived")
					return ExecutionResult{}, nil
				}),
				WorkerConfig{},
			)
			require.NoError(t, worker.ProcessOnce(t.Context()))
			assert.Empty(t, store.failed)
			assert.Empty(t, store.attemptFailures)
		})
	}
}

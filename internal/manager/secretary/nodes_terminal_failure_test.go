package secretary

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeTerminalFailureGivenVerifiedUnsuccessfulExecutionThenFailsWithoutSavingPartialOutput(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	failure := errors.Join(domain.ErrSecretaryExecutionFailed, errors.New("verified cancellation"))
	worker := NewWorker(
		store,
		ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			store.events = append(store.events, "execute")
			return ExecutionResult{Text: "partial response"}, failure
		}),
		WorkerConfig{},
	)
	err := worker.ProcessOnce(t.Context())
	require.ErrorIs(t, err, domain.ErrSecretaryExecutionFailed)
	assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
	assert.Equal(t, []string{"prepare", "begin", "execute", "fail-attempt"}, store.events)
	require.Len(t, store.attemptFailures, 1)
	assert.ErrorIs(t, store.attemptFailures[0], domain.ErrSecretaryExecutionFailed)
	assert.NotErrorIs(t, store.attemptFailures[0], domain.ErrSecretaryExecutionUnknown)
	assert.Empty(t, store.savedText)
	assert.Empty(t, store.published)
}

func TestNodeTerminalFailureGivenVerifiedOriginalCancellationThenRecoveryOnlyRecordsFailure(
	t *testing.T,
) {
	store := newNodeWorkerStore()
	worker := NewWorker(store, nodeRecoveringExecutor{
		Executor: ExecutorFunc(func(context.Context, ExecutionRequest) (ExecutionResult, error) {
			t.Fatal("recovering the canceled attempt must not dispatch another request")
			return ExecutionResult{}, nil
		}),
		recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
			store.events = append(store.events, "recover")
			return ExecutionResult{
				Text: "partial response",
			}, false, domain.ErrSecretaryExecutionFailed
		},
	}, WorkerConfig{})
	err := worker.RecoverJob(t.Context(), taskRecoveryJob("unknown"))
	require.ErrorIs(t, err, domain.ErrSecretaryExecutionFailed)
	assert.NotErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
	assert.Equal(t, []string{"recover", "fail-attempt"}, store.events)
	assert.Empty(t, store.savedText)
	assert.Empty(t, store.published)
}

func TestNodeTerminalFailureGivenUnconfirmedCancellationOrConflictingSignalsThenRemainsUnknown(
	t *testing.T,
) {
	for _, failure := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("cancel notification was sent"),
		errors.Join(domain.ErrSecretaryExecutionFailed, domain.ErrSecretaryExecutionUnknown),
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			store := newNodeWorkerStore()
			worker := NewWorker(store, nodeRecoveringExecutor{
				Executor: ExecutorFunc(
					func(context.Context, ExecutionRequest) (ExecutionResult, error) {
						t.Fatal("an unconfirmed execution must never be replayed")
						return ExecutionResult{}, nil
					},
				),
				recover: func(context.Context, ExecutionRequest) (ExecutionResult, bool, error) {
					return ExecutionResult{Text: "partial response"}, false, failure
				},
			}, WorkerConfig{})
			err := worker.RecoverJob(t.Context(), taskRecoveryJob("unknown"))
			require.ErrorIs(t, err, domain.ErrSecretaryExecutionUnknown)
			assert.Empty(t, store.events)
			assert.Empty(t, store.attemptFailures)
			assert.Empty(t, store.savedText)
			assert.Empty(t, store.published)
		})
	}
}

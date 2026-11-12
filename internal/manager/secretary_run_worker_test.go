package manager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type secretaryRunWorkerStore struct {
	Store
	advance func(context.Context, int) (bool, error)
}

func (s *secretaryRunWorkerStore) AdvanceSecretaryRuns(
	ctx context.Context,
	limit int,
) (bool, error) {
	return s.advance(ctx, limit)
}

func TestSecretaryRunWorkerGivenNoAgentOrSearchThenStillDrainsBoundedCommands(t *testing.T) {
	srv := &Service{}
	srv.startSecretaryRunWorker(t.Context(), nil)()
	called := make(chan int, 1)
	stop := srv.startSecretaryRunWorker(
		t.Context(),
		&secretaryRunWorkerStore{advance: func(ctx context.Context, limit int) (bool, error) {
			deadline, ok := ctx.Deadline()
			assert.True(t, ok)
			assert.InDelta(t, 5, time.Until(deadline).Seconds(), 0.5)
			select {
			case called <- limit:
			case <-ctx.Done():
			}
			return false, nil
		}},
	)
	t.Cleanup(stop)
	assert.Equal(t, 128, receiveSecretaryProgressWorker(t, called))
	stop()
	stop()
}

func TestSecretaryRunWorkerGivenDatabaseFailureThenUsesCancellableBackoff(t *testing.T) {
	called := make(chan int, 1)
	stop := (&Service{}).startSecretaryRunWorker(
		t.Context(),
		&secretaryRunWorkerStore{advance: func(ctx context.Context, limit int) (bool, error) {
			select {
			case called <- limit:
			case <-ctx.Done():
			}
			return false, assert.AnError
		}},
	)
	t.Cleanup(stop)
	assert.Equal(t, 128, receiveSecretaryProgressWorker(t, called))
	assert.Equal(t, 128, receiveSecretaryProgressWorker(t, called))
	stop()
}

func TestSecretaryRunWorkerGivenEmptyQueueThenWaitsBeforePollingAgain(t *testing.T) {
	waits := newSecretaryProgressWorkerWait()
	stop := startSecretaryMaintenanceWorker(
		t.Context(),
		func(context.Context, int) (bool, error) { return false, nil },
		secretaryProgressWorkerConfig{wait: waits.wait},
	)
	t.Cleanup(stop)
	assert.Equal(t, time.Second, receiveSecretaryProgressWorker(t, waits.entered))
}

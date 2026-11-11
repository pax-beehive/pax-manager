package manager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type secretaryProgressWorkerStore struct {
	Store
	advance func(context.Context, int) (bool, error)
}

func (s *secretaryProgressWorkerStore) AdvanceSecretaryProgress(
	ctx context.Context,
	limit int,
) (bool, error) {
	return s.advance(ctx, limit)
}

type secretaryProgressWorkerWait struct {
	entered chan time.Duration
	release chan struct{}
}

func newSecretaryProgressWorkerWait() *secretaryProgressWorkerWait {
	return &secretaryProgressWorkerWait{
		entered: make(chan time.Duration),
		release: make(chan struct{}),
	}
}

func (w *secretaryProgressWorkerWait) wait(ctx context.Context, delay time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case w.entered <- delay:
	}
	select {
	case <-ctx.Done():
		return false
	case <-w.release:
		return true
	}
}

func receiveSecretaryProgressWorker[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("progress worker did not reach the expected lifecycle boundary")
		var zero T
		return zero
	}
}

func TestSecretaryProgressWorkerGivenDisabledSecretaryAndNoSearchThenRunsWithBoundedWork(
	t *testing.T,
) {
	srv := &Service{}
	srv.startSecretaryProgressWorker(t.Context(), nil)()
	called := make(chan int, 1)
	store := &secretaryProgressWorkerStore{
		advance: func(ctx context.Context, limit int) (bool, error) {
			deadline, ok := ctx.Deadline()
			assert.True(t, ok)
			assert.InDelta(t, 5, time.Until(deadline).Seconds(), 0.5)
			select {
			case called <- limit:
			case <-ctx.Done():
				return false, ctx.Err()
			}
			return false, nil
		},
	}
	stop := srv.startSecretaryProgressWorker(t.Context(), store)
	t.Cleanup(stop)
	assert.Equal(t, 128, receiveSecretaryProgressWorker(t, called))
	stop()
	stop()
}

func TestSecretaryProgressWorkerGivenBacklogThenWaitsBetweenEveryBoundedAdvance(t *testing.T) {
	waits := newSecretaryProgressWorkerWait()
	var calls atomic.Int32
	store := &secretaryProgressWorkerStore{
		advance: func(_ context.Context, limit int) (bool, error) {
			assert.Equal(t, 128, limit)
			calls.Add(1)
			return true, nil
		},
	}
	stop := startSecretaryProgressProjectionWorker(
		t.Context(),
		store,
		secretaryProgressWorkerConfig{wait: waits.wait},
	)
	t.Cleanup(stop)
	assert.Equal(t, 100*time.Millisecond, receiveSecretaryProgressWorker(t, waits.entered))
	assert.EqualValues(t, 1, calls.Load(), "more work must not bypass the polling boundary")
	waits.release <- struct{}{}
	assert.Equal(t, 100*time.Millisecond, receiveSecretaryProgressWorker(t, waits.entered))
	assert.EqualValues(t, 2, calls.Load())
}

func TestSecretaryProgressWorkerGivenStorageFailuresThenBacksOffAndResetsAfterSuccess(
	t *testing.T,
) {
	waits := newSecretaryProgressWorkerWait()
	failure := errors.New("database unavailable")
	var calls atomic.Int32
	failures := make(chan error, 16)
	store := &secretaryProgressWorkerStore{advance: func(context.Context, int) (bool, error) {
		if calls.Add(1) <= 9 {
			return false, failure
		}
		return true, nil
	}}
	stop := startSecretaryProgressProjectionWorker(
		t.Context(),
		store,
		secretaryProgressWorkerConfig{
			wait:    waits.wait,
			onError: func(err error) { failures <- err },
		},
	)
	t.Cleanup(stop)
	delays := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond,
		6400 * time.Millisecond, 10 * time.Second, 10 * time.Second,
		100 * time.Millisecond,
	}
	for index, delay := range delays {
		assert.Equal(t, delay, receiveSecretaryProgressWorker(t, waits.entered))
		assert.EqualValues(t, index+1, calls.Load())
		if index < 9 {
			assert.ErrorIs(t, receiveSecretaryProgressWorker(t, failures), failure)
		}
		if index < len(delays)-1 {
			waits.release <- struct{}{}
		}
	}
}

func TestSecretaryProgressWorkerGivenCanceledContextThenDoesNotAdvance(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var calls atomic.Int32
	store := &secretaryProgressWorkerStore{advance: func(context.Context, int) (bool, error) {
		calls.Add(1)
		return false, nil
	}}
	stop := startSecretaryProgressProjectionWorker(ctx, store, secretaryProgressWorkerConfig{})
	stop()
	assert.Zero(t, calls.Load())
}

func TestSecretaryProgressWorkerGivenInFlightAdvanceThenStopCancelsAndWaitsForCompletion(
	t *testing.T,
) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	store := &secretaryProgressWorkerStore{advance: func(ctx context.Context, _ int) (bool, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return false, ctx.Err()
	}}
	stop := startSecretaryProgressProjectionWorker(
		t.Context(),
		store,
		secretaryProgressWorkerConfig{},
	)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		stop()
	})
	receiveSecretaryProgressWorker(t, started)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	receiveSecretaryProgressWorker(t, canceled)
	select {
	case <-stopped:
		t.Fatal("stop returned before the in-flight storage call completed")
	default:
	}
	close(release)
	receiveSecretaryProgressWorker(t, stopped)
	stop()
}

func TestSecretaryProgressWorkerGivenAdvanceDeadlineThenRetriesWithoutBlockingShutdown(
	t *testing.T,
) {
	waits := newSecretaryProgressWorkerWait()
	failures := make(chan error, 1)
	store := &secretaryProgressWorkerStore{advance: func(ctx context.Context, _ int) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	}}
	stop := startSecretaryProgressProjectionWorker(
		t.Context(),
		store,
		secretaryProgressWorkerConfig{
			stepTimeout: time.Millisecond,
			wait:        waits.wait,
			onError:     func(err error) { failures <- err },
		},
	)
	t.Cleanup(stop)
	assert.ErrorIs(t, receiveSecretaryProgressWorker(t, failures), context.DeadlineExceeded)
	assert.Equal(t, 100*time.Millisecond, receiveSecretaryProgressWorker(t, waits.entered))
}

func TestSecretaryProgressWorkerGivenPollTimerThenHonorsDelayAndCancellation(t *testing.T) {
	require.True(t, waitSecretaryProgressWorker(t.Context(), time.Nanosecond))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.False(t, waitSecretaryProgressWorker(ctx, time.Hour))
}

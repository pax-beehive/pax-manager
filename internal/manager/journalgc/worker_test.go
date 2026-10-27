package journalgc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type storeFunc func(context.Context, time.Time, int, int64) (int64, error)

func (f storeFunc) PruneCompletedTransportFrames(
	ctx context.Context, cutoff time.Time, limit int, tail int64,
) (int64, error) {
	return f(ctx, cutoff, limit, tail)
}

func TestGivenOldHistoryWhenWorkerTicksThenPruneBoundedBatchesAndRetryErrors(t *testing.T) {
	reports := make(chan error, 4)
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	calls := 0
	store := storeFunc(
		func(ctx context.Context, cutoff time.Time, limit int, tail int64) (int64, error) {
			assert.Equal(t, now.Add(-72*time.Hour), cutoff)
			assert.Equal(t, 1000, limit)
			assert.Equal(t, int64(100), tail)
			_, ok := ctx.Deadline()
			assert.True(t, ok)
			calls++
			if calls == 1 {
				return 0, errors.New("temporarily busy")
			}
			return 17, nil
		},
	)
	stop, err := Start(context.Background(), store, Options{
		Interval: time.Millisecond, Now: func() time.Time { return now },
		Report: func(deleted int64, err error) {
			if err == nil {
				assert.Equal(t, int64(17), deleted)
			}
			select {
			case reports <- err:
			default:
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	select {
	case err := <-reports:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	select {
	case err := <-reports:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("cleanup did not retry")
	}
	stop()
}

func TestGivenShutdownWhenBatchIsRunningThenCancelAndJoin(t *testing.T) {
	started := make(chan struct{})
	store := storeFunc(func(ctx context.Context, _ time.Time, _ int, _ int64) (int64, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	stop, err := Start(context.Background(), store, Options{Interval: time.Millisecond})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	stop()
	stop()
}

func TestGivenSlowBatchThenTimeoutAndContinueOnNextTick(t *testing.T) {
	reported := make(chan error, 1)
	store := storeFunc(func(ctx context.Context, _ time.Time, _ int, _ int64) (int64, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	stop, err := Start(context.Background(), store, Options{
		Interval: time.Millisecond, Timeout: time.Millisecond,
		Report: func(_ int64, err error) {
			select {
			case reported <- err:
			default:
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	select {
	case err := <-reported:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("batch did not time out")
	}
}

func TestGivenInvalidPolicyThenRefuseToStart(t *testing.T) {
	for _, options := range []Options{
		{Retention: -1}, {Interval: -1}, {Timeout: -1}, {BatchSize: -1},
		{BatchSize: 10001}, {KeepTail: -1},
	} {
		_, err := Start(context.Background(), nil, options)
		require.Error(t, err)
	}
	_, err := Start(context.Background(), nil, Options{})
	require.Error(t, err)
}

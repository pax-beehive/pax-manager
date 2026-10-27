package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type gcTestStore struct {
	Store
	prune func(context.Context, time.Time, int, int64) (int64, error)
}

func (s gcTestStore) PruneCompletedTransportFrames(
	ctx context.Context, cutoff time.Time, limit int, tail int64,
) (int64, error) {
	return s.prune(ctx, cutoff, limit, tail)
}

func TestGivenConfiguredStoreThenRunJournalCleanup(t *testing.T) {
	for _, result := range []int64{0, 3, -1} {
		called := make(chan struct{}, 1)
		store := gcTestStore{prune: func(context.Context, time.Time, int, int64) (int64, error) {
			select {
			case called <- struct{}{}:
			default:
			}
			if result < 0 {
				return 0, errors.New("test cleanup error")
			}
			return result, nil
		}}
		stop, err := startTransportJournalGC(context.Background(), store, Config{
			TransportJournalGCInterval: time.Millisecond,
		})
		require.NoError(t, err)
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("cleanup did not start")
		}
		stop()
	}
}

func TestGivenDisabledOrUnsupportedStoreThenNoJournalWorker(t *testing.T) {
	stop, err := startTransportJournalGC(context.Background(), nil, Config{})
	require.NoError(t, err)
	stop()
	stop, err = startTransportJournalGC(context.Background(), gcTestStore{}, Config{
		TransportJournalGCDisabled: true,
	})
	require.NoError(t, err)
	stop()
	_, err = startTransportJournalGC(context.Background(), gcTestStore{}, Config{
		TransportJournalGCBatchSize: 10001,
	})
	require.Error(t, err)
}

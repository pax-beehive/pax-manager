package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/storage"
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
		cfg := Config{TransportJournalGCInterval: time.Millisecond}
		stop, err := startTransportJournalGC(
			context.Background(), store, cfg, newRuntimeConfigStore(cfg, nil),
		)
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
	stop, err := startTransportJournalGC(context.Background(), nil, Config{}, nil)
	require.NoError(t, err)
	stop()
	_, err = startTransportJournalGC(context.Background(), gcTestStore{}, Config{
		TransportJournalGCBatchSize: 10001,
	}, nil)
	require.Error(t, err)
}

func TestJournalCleanupGivenDisabledFlagThenPausesUntilOverrideEnablesIt(t *testing.T) {
	called := make(chan struct{}, 1)
	store := gcTestStore{prune: func(context.Context, time.Time, int, int64) (int64, error) {
		select {
		case called <- struct{}{}:
		default:
		}
		return 0, nil
	}}
	cfg := Config{TransportJournalGCDisabled: true, TransportJournalGCInterval: time.Millisecond}
	config := newRuntimeConfigStore(cfg, storage.NewMemoryStore(time.Now))
	stop, err := startTransportJournalGC(context.Background(), store, cfg, config)
	require.NoError(t, err)
	defer stop()
	select {
	case <-called:
		t.Fatal("cleanup ran while TRANSPORT_JOURNAL_GC_DISABLED is true")
	case <-time.After(50 * time.Millisecond):
	}
	_, err = config.Set(t.Context(), "transport.journal_gc_disabled", []byte(`false`), 0,
		"resume cleanup", "admin@example.com")
	require.NoError(t, err)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not resume after the override")
	}
}

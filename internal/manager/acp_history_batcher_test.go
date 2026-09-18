package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type orderedHistoryTestStore struct {
	domain.Store
	appendText func(string) error
}

func (s *orderedHistoryTestStore) AppendMessagePartText(
	_ context.Context, _ string, _ int, delta string, _ []byte,
) error {
	return s.appendText(delta)
}

func TestACPHistoryTextBatcherSerializesWrites(t *testing.T) {
	for _, second := range []string{"flush", "threshold", "completion"} {
		t.Run(second, func(t *testing.T) {
			ctx := context.Background()
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var mu sync.Mutex
			text := ""
			store := &orderedHistoryTestStore{appendText: func(delta string) error {
				if delta == "p" {
					close(entered)
					<-release
				}
				mu.Lock()
				text += delta
				mu.Unlock()
				return nil
			}}
			batcher := newACPHistoryTextBatcher(store, time.Hour, 2)
			require.NoError(t, batcher.AppendText(ctx, "msg", 0, "p"))
			firstDone := make(chan error, 1)
			go func() { firstDone <- batcher.Flush(ctx) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("first write did not start")
			}
			secondDone := make(chan error, 1)
			go func() {
				if second == "threshold" {
					secondDone <- batcher.AppendText(ctx, "msg", 0, "ax")
					return
				}
				if second == "flush" {
					if err := batcher.AppendText(ctx, "msg", 0, "a"); err != nil {
						secondDone <- err
						return
					}
				}
				secondDone <- batcher.Flush(ctx)
			}()
			finishedEarly := false
			select {
			case err := <-secondDone:
				require.NoError(t, err)
				finishedEarly = true
				t.Error("later flush completed before the earlier write")
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			require.NoError(t, <-firstDone)
			if !finishedEarly {
				require.NoError(t, <-secondDone)
			}
			require.NoError(t, batcher.Flush(ctx))
			mu.Lock()
			got := text
			mu.Unlock()
			want := map[string]string{"flush": "pa", "threshold": "pax", "completion": "p"}[second]
			require.Equal(t, want, got)
		})
	}
}

func TestACPHistoryTextBatcherRetriesBeforeLaterText(t *testing.T) {
	ctx := context.Background()
	writeErr := errors.New("write failed")
	var calls []string
	store := &orderedHistoryTestStore{appendText: func(delta string) error {
		calls = append(calls, delta)
		if len(calls) == 1 {
			return writeErr
		}
		return nil
	}}
	batcher := newACPHistoryTextBatcher(store, time.Hour, 2)
	require.NoError(t, batcher.AppendText(ctx, "msg", 0, "p"))
	require.ErrorIs(t, batcher.Flush(ctx), writeErr)
	require.NoError(t, batcher.AppendText(ctx, "msg", 0, "ax"))
	require.NoError(t, batcher.Flush(ctx))
	require.Equal(t, []string{"p", "pax"}, calls)
}

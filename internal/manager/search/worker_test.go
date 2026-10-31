package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type fakeQueue struct {
	work        []domain.SearchWork
	backfillErr error
	claimErr    error
	completeErr error
	completed   []domain.SearchWork
	syncErrors  []error
	backfills   int
}

func (q *fakeQueue) BackfillSearch(context.Context) error { q.backfills++; return q.backfillErr }

func (q *fakeQueue) ClaimSearchBatch(
	context.Context,
	int,
	time.Duration,
) ([]domain.SearchWork, error) {
	return q.work, q.claimErr
}
func (q *fakeQueue) CompleteSearchWork(_ context.Context, w domain.SearchWork, e error) error {
	q.completed = append(q.completed, w)
	q.syncErrors = append(q.syncErrors, e)
	return q.completeErr
}

type fakeBackend struct {
	indexErr, errorEnsure error
	indexed               []domain.SearchDocument
	ensures               int
}

func (b *fakeBackend) EnsureIndex(context.Context) error { b.ensures++; return b.errorEnsure }
func (b *fakeBackend) Index(_ context.Context, d domain.SearchDocument) error {
	b.indexed = append(b.indexed, d)
	return b.indexErr
}
func (b *fakeBackend) Search(context.Context, domain.SearchQuery) (domain.SearchPage, error) {
	return domain.SearchPage{}, nil
}

func TestWorkerCompletesTheExactClaimedRevisionAndPersistsFailures(t *testing.T) {
	failure := errors.New("offline")
	for _, indexErr := range []error{nil, failure} {
		t.Run("process", func(t *testing.T) {
			work := domain.SearchWork{
				Document:   domain.SearchDocument{Kind: "message", SourceID: "one", Revision: 9},
				LeaseToken: "lease",
			}
			q := &fakeQueue{work: []domain.SearchWork{work}}
			b := &fakeBackend{indexErr: indexErr}
			w := NewWorker(q, b, WorkerConfig{})
			err := w.ProcessOnce(context.Background())
			assert.ErrorIs(t, err, indexErr)
			assert.Equal(t, []domain.SearchWork{work}, q.completed)
			assert.Equal(t, []domain.SearchDocument{work.Document}, b.indexed)
			assert.Equal(t, []error{indexErr}, q.syncErrors)
		})
	}
}
func TestWorkerReturnsQueueFailures(t *testing.T) {
	failure := errors.New("queue unavailable")
	q := &fakeQueue{claimErr: failure}
	w := NewWorker(q, &fakeBackend{}, WorkerConfig{})
	assert.ErrorIs(t, w.ProcessOnce(context.Background()), failure)
	q.claimErr = nil
	q.completeErr = failure
	q.work = []domain.SearchWork{{Document: domain.SearchDocument{Revision: 1}}}
	assert.ErrorIs(t, w.ProcessOnce(context.Background()), failure)
}
func TestWorkerRetriesInitializationWithoutBlockingStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := &fakeQueue{backfillErr: errors.New("backfill temporarily failed")}
	b := &fakeBackend{errorEnsure: errors.New("backend unavailable")}
	failures := 0
	w := NewWorker(
		q,
		b,
		WorkerConfig{
			PollInterval:  time.Millisecond,
			RetryInterval: time.Millisecond,
			OnError: func(error) {
				failures++
				if failures == 1 {
					b.errorEnsure = nil
				}
				if failures == 2 {
					q.backfillErr = nil
				}
				if failures == 3 {
					cancel()
				}
			},
		},
	)
	q.claimErr = errors.New("queue temporarily failed")
	require.ErrorIs(t, w.Run(ctx), context.Canceled)
	assert.GreaterOrEqual(t, b.ensures, 2)
	assert.Equal(t, 2, q.backfills)
	assert.Equal(t, 3, failures)
}
func TestWorkerHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := &fakeQueue{work: []domain.SearchWork{{}, {}}}
	b := &fakeBackend{}
	w := NewWorker(q, b, WorkerConfig{})
	assert.ErrorIs(t, w.Run(ctx), context.Canceled)
	assert.ErrorIs(t, w.ProcessOnce(ctx), context.Canceled)
	assert.Empty(t, b.indexed)
}

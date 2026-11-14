package secretary

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// singleClaimStore hands out one job per claim, like a busy shared queue.
type singleClaimStore struct {
	*workerStore
	queue  []domain.SecretaryJob
	claims int
}

func (s *singleClaimStore) ClaimSecretaryJobs(
	context.Context,
	int,
	time.Duration,
	time.Duration,
) ([]domain.SecretaryJob, error) {
	s.claims++
	if len(s.queue) == 0 {
		return nil, nil
	}
	job := s.queue[0]
	s.queue = s.queue[1:]
	return []domain.SecretaryJob{job}, nil
}

func ownedJob(owner string) domain.SecretaryJob {
	job := testJob()
	job.OwnerUserID = owner
	return job
}

func TestWorkerGivenFeatureDisabledOwnersThenSkipsTheirJobsWithoutFailingThem(t *testing.T) {
	store := &singleClaimStore{
		workerStore: newWorkerStore(),
		queue: []domain.SecretaryJob{
			ownedJob("member-1"),
			ownedJob("member-2"),
			ownedJob("admin"),
		},
	}
	var asked []string
	worker := NewWorker(store, nil, WorkerConfig{
		OwnerAllowed: func(_ context.Context, owner string) bool {
			asked = append(asked, owner)
			return owner == "admin"
		},
	})
	// The allowed job runs (and fails here because no executor is configured).
	require.Error(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, []string{"member-1", "member-2", "admin"}, asked)
	assert.Equal(t, 3, store.claims)
	assert.Len(t, store.failed, 1, "skipped jobs are never failed")
	assert.Empty(t, store.publications)
}

func TestWorkerGivenOnlyDisabledOwnersThenBoundsClaimsPerPass(t *testing.T) {
	queue := make([]domain.SecretaryJob, 0, 100)
	for range 100 {
		queue = append(queue, ownedJob("member"))
	}
	store := &singleClaimStore{workerStore: newWorkerStore(), queue: queue}
	worker := NewWorker(store, nil, WorkerConfig{
		OwnerAllowed: func(context.Context, string) bool { return false },
	})
	require.NoError(t, worker.ProcessOnce(t.Context()))
	assert.Equal(t, maxSkippedClaims, store.claims)
	assert.Empty(t, store.failed)
}

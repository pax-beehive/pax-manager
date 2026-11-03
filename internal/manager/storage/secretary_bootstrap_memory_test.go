package storage

import (
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretaryBootstrapMemoryGivenExistingWorkThenRestartsPreserveDurableScheduling(
	t *testing.T,
) {
	for _, state := range []string{"completed", "pending", "leased", "retry_deferred"} {
		t.Run(state, func(t *testing.T) {
			store, now := secretaryMemoryFixture(t)
			secretaryEnable(t, store)
			first := secretaryClaim(t, store)
			version, err := store.PublishSecretaryJob(
				t.Context(),
				first,
				secretaryPublication(first),
			)
			require.NoError(t, err)
			if state != "completed" {
				secretaryMemoryMessage(store, "m2", "New user-visible content", 2)
				require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
			}
			if state == "leased" || state == "retry_deferred" {
				work := secretaryClaim(t, store)
				if state == "retry_deferred" {
					require.NoError(t, store.FailSecretaryJob(t.Context(), work, assert.AnError))
				}
			}
			job := store.secretaryState.jobs[first.SourceID]
			desired, processed := job.desired, job.processed
			updated, available, leaseUntil := job.updated, job.available, job.leaseUntil
			lease, lastError := job.work.LeaseToken, job.lastError
			changes := maps.Clone(job.changes)
			seen := maps.Clone(job.seen)
			checkpoint := maps.Clone(job.checkpoint)
			status, err := store.GetSecretaryStatus(t.Context(), "owner")
			require.NoError(t, err)

			for range 3 {
				*now = now.Add(time.Second)
				require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
			}

			assert.Equal(t, desired, job.desired)
			assert.Equal(t, processed, job.processed)
			assert.Equal(t, updated, job.updated)
			assert.Equal(t, available, job.available)
			assert.Equal(t, leaseUntil, job.leaseUntil)
			assert.Equal(t, lease, job.work.LeaseToken)
			assert.Equal(t, lastError, job.lastError)
			assert.Equal(t, changes, job.changes)
			assert.Equal(t, seen, job.seen)
			assert.Equal(t, checkpoint, job.checkpoint)
			after, err := store.GetSecretaryStatus(t.Context(), "owner")
			require.NoError(t, err)
			assert.Equal(t, status.PendingCount, after.PendingCount)
			assert.Equal(t, status.LastCompletedAt, after.LastCompletedAt)
			assert.Len(t, store.secretaryState.summaries[version.SummaryID], 1)
		})
	}
}

func TestSecretaryBootstrapMemoryGivenMissingSourceThenSeedsWorkOnce(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	clear(store.secretaryState.jobs)

	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	status, err := store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
	work := secretaryClaim(t, store)
	require.Len(t, work.Messages, 1)
	assert.Equal(t, "m1", work.Messages[0].SourceID)
	_, err = store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)

	require.NoError(t, store.BackfillSecretaryJobs(t.Context()))
	status, err = store.GetSecretaryStatus(t.Context(), "owner")
	require.NoError(t, err)
	assert.Zero(t, status.PendingCount)
}

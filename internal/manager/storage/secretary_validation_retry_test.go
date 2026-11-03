package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryValidationRetryGivenInvalidCachedOutputThenUsesBoundedFreshRunIDs(t *testing.T) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	previous := ""
	for attempt := range 5 {
		work := secretaryClaim(t, store)
		if attempt > 0 && attempt <= domain.SecretaryMaxValidationRetries {
			assert.NotEqual(t, previous, work.RunID)
		}
		if attempt > domain.SecretaryMaxValidationRetries {
			assert.Equal(t, previous, work.RunID)
		}
		if attempt > 0 {
			assert.Equal(t, "evidence", work.ValidationFeedback)
		}
		previous = work.RunID
		require.NoError(
			t,
			store.FailSecretaryJob(
				t.Context(),
				work,
				domain.SecretaryOutputValidationError{
					Code: "evidence",
					Err: errors.New(
						"secretary evidence was not present in the supplied or verified text",
					),
				},
			),
		)
		job := store.secretaryLocked().jobs[work.SourceID]
		assert.Equal(t, min(attempt+1, domain.SecretaryMaxValidationRetries), job.autoRetryCount)
		assert.EqualValues(
			t,
			min(attempt+1, domain.SecretaryMaxValidationRetries),
			job.work.RetryGeneration,
		)
		*now = now.Add(6 * time.Second)
	}
}

func TestSecretaryValidationRetryGivenTransientFailureOrStaleLeaseThenDoesNotSpendFreshBudget(
	t *testing.T,
) {
	for _, failure := range []error{context.DeadlineExceeded, domain.ErrSecretaryFenced, errors.New("secretary execution is incomplete; waiting for its existing turn"), errors.New("offline")} {
		store, _ := secretaryMemoryFixture(t)
		secretaryEnable(t, store)
		work := secretaryClaim(t, store)
		require.NoError(t, store.FailSecretaryJob(t.Context(), work, failure))
		assert.Zero(t, store.secretaryLocked().jobs[work.SourceID].autoRetryCount)
		assert.Zero(t, store.secretaryLocked().jobs[work.SourceID].work.RetryGeneration)
	}
	store, _ := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	stale := work
	stale.LeaseToken = "old-lease"
	require.NoError(
		t,
		store.FailSecretaryJob(
			t.Context(),
			stale,
			domain.SecretaryOutputValidationError{
				Code: "evidence",
				Err:  errors.New("secretary evidence quote is empty"),
			},
		),
	)
	assert.Zero(t, store.secretaryLocked().jobs[work.SourceID].autoRetryCount)
	assert.Equal(t, work.LeaseToken, store.secretaryLocked().jobs[work.SourceID].work.LeaseToken)
}

func TestSecretaryValidationRetryGivenNewInputManualRetryOrSuccessThenResetsTheBudget(
	t *testing.T,
) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	for range domain.SecretaryMaxValidationRetries {
		work := secretaryClaim(t, store)
		require.NoError(
			t,
			store.FailSecretaryJob(
				t.Context(),
				work,
				domain.SecretaryOutputValidationError{
					Code: "evidence",
					Err:  errors.New("secretary evidence quote is empty"),
				},
			),
		)
		*now = now.Add(6 * time.Second)
	}
	secretaryMemoryMessage(store, "m2", "fresh input", 2)
	work := secretaryClaim(t, store)
	require.NoError(
		t,
		store.FailSecretaryJob(
			t.Context(),
			work,
			domain.SecretaryOutputValidationError{
				Code: "evidence",
				Err:  errors.New("secretary evidence quote is empty"),
			},
		),
	)
	assert.Equal(t, 1, store.secretaryLocked().jobs[work.SourceID].autoRetryCount)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	assert.Zero(t, store.secretaryLocked().jobs[work.SourceID].autoRetryCount)
	work = secretaryClaim(t, store)
	_, err := store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	assert.Zero(t, store.secretaryLocked().jobs[work.SourceID].autoRetryCount)
}

func TestSecretaryValidationRetryGivenExplicitRetryDuringFreshActiveRunThenPreservesItsBudgetAndLease(
	t *testing.T,
) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	old := secretaryClaim(t, store)
	require.NoError(t, store.FailSecretaryJob(t.Context(), old, validationFailure()))
	*now = now.Add(6 * time.Second)
	current := secretaryClaim(t, store)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	require.NoError(t, store.FailSecretaryJob(t.Context(), old, validationFailure()))
	job := store.secretaryLocked().jobs[current.SourceID]
	assert.Equal(t, 1, job.autoRetryCount)
	assert.Equal(t, current.LeaseToken, job.work.LeaseToken)
	assert.Equal(t, current.RunID, job.work.RunID)
	assert.Equal(t, "evidence", job.work.ValidationFeedback)
}

func TestSecretaryValidationRetryGivenExpiredLeaseThenCannotSpendBudgetUntilClaimedAgain(
	t *testing.T,
) {
	store, now := secretaryMemoryFixture(t)
	secretaryEnable(t, store)
	old := secretaryClaim(t, store)
	*now = now.Add(2 * time.Minute)
	require.NoError(t, store.FailSecretaryJob(t.Context(), old, validationFailure()))
	job := store.secretaryLocked().jobs[old.SourceID]
	assert.Zero(t, job.autoRetryCount)
	assert.Zero(t, job.work.RetryGeneration)
	assert.Empty(t, job.lastError)
	current := secretaryClaim(t, store)
	assert.NotEqual(t, old.LeaseToken, current.LeaseToken)
	require.NoError(t, store.FailSecretaryJob(t.Context(), current, validationFailure()))
	assert.Equal(t, 1, job.autoRetryCount)
	assert.EqualValues(t, 1, job.work.RetryGeneration)
}

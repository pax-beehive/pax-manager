package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func validationFailure() error {
	return domain.SecretaryOutputValidationError{
		Code: "evidence",
		Err:  errors.New("invalid generated evidence"),
	}
}

func releaseSecretaryBackoff(t *testing.T, store *PostgresStore) {
	t.Helper()
	_, err := store.db.ExecContext(t.Context(), `UPDATE secretary_jobs SET available_at=NOW()`)
	require.NoError(t, err)
}

func readSecretaryRetryState(
	t *testing.T,
	store *PostgresStore,
	source string,
) (int64, int, string) {
	t.Helper()
	var generation int64
	var count int
	var key string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT retry_generation,auto_retry_count,auto_retry_key FROM secretary_jobs WHERE source_id=$1`, source).
			Scan(&generation, &count, &key),
	)
	return generation, count, key
}

func TestSecretaryValidationRetryPostgresGivenInvalidCompletedOutputThenFreshBudgetSurvivesStoreRestart(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	previous := ""
	for attempt := range 5 {
		store = NewPostgresStore(store.db, time.Now)
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
		require.NoError(t, store.FailSecretaryJob(t.Context(), work, validationFailure()))
		generation, count, key := readSecretaryRetryState(t, store, work.SourceID)
		assert.EqualValues(t, min(attempt+1, domain.SecretaryMaxValidationRetries), generation)
		assert.Equal(t, min(attempt+1, domain.SecretaryMaxValidationRetries), count)
		assert.Equal(t, secretaryValidationRetryKey(work), key)
		releaseSecretaryBackoff(t, store)
	}
}

func TestSecretaryValidationRetryPostgresGivenChangedInputOrManualRetryOrPublicationThenHasFreshBudget(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	for range domain.SecretaryMaxValidationRetries {
		work := secretaryClaim(t, store)
		require.NoError(t, store.FailSecretaryJob(t.Context(), work, validationFailure()))
		releaseSecretaryBackoff(t, store)
	}
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE message_parts SET text='new canonical input' WHERE message_id='m1'`,
	)
	require.NoError(t, err)
	work := secretaryClaim(t, store)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, validationFailure()))
	_, count, _ := readSecretaryRetryState(t, store, work.SourceID)
	assert.Equal(t, 1, count)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	_, count, key := readSecretaryRetryState(t, store, work.SourceID)
	assert.Zero(t, count)
	assert.Empty(t, key)
	work = secretaryClaim(t, store)
	assert.Empty(t, work.ValidationFeedback)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, validationFailure()))
	releaseSecretaryBackoff(t, store)
	work = secretaryClaim(t, store)
	_, err = store.PublishSecretaryJob(t.Context(), work, secretaryPublication(work))
	require.NoError(t, err)
	_, count, key = readSecretaryRetryState(t, store, work.SourceID)
	assert.Zero(t, count)
	assert.Empty(t, key)
	var feedback string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT last_validation_failure FROM secretary_jobs WHERE source_id=$1`, work.SourceID).
			Scan(&feedback),
	)
	assert.Empty(t, feedback)
}

func TestSecretaryValidationRetryPostgresGivenStaleLeaseDisabledSettingsOrTransientErrorThenCannotSpendBudget(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	cfg := secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	stale := work
	stale.LeaseToken = "stale"
	require.NoError(t, store.FailSecretaryJob(t.Context(), stale, validationFailure()))
	generation, count, key := readSecretaryRetryState(t, store, work.SourceID)
	assert.Zero(t, generation)
	assert.Zero(t, count)
	assert.Empty(t, key)
	active, err := store.SecretaryJobActive(t.Context(), work)
	require.NoError(t, err)
	assert.True(t, active)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, context.DeadlineExceeded))
	generation, count, key = readSecretaryRetryState(t, store, work.SourceID)
	assert.Zero(t, generation)
	assert.Zero(t, count)
	assert.Empty(t, key)
	releaseSecretaryBackoff(t, store)
	work = secretaryClaim(t, store)
	_, err = store.UpdateSecretarySettings(
		t.Context(),
		"owner",
		domain.SecretarySettingsUpdate{AgentID: "secretary", ExpectedRevision: cfg.Revision},
	)
	require.NoError(t, err)
	require.NoError(t, store.FailSecretaryJob(t.Context(), work, validationFailure()))
	generation, count, key = readSecretaryRetryState(t, store, work.SourceID)
	assert.Zero(t, generation)
	assert.Zero(t, count)
	assert.Empty(t, key)
}

func TestSecretaryValidationRetryDriverGivenTypedFailureThenSavesBoundedBudgetUnderMatchingLease(
	t *testing.T,
) {
	for _, failure := range []error{validationFailure(), domain.ErrSecretaryInvalidReadReceipt, context.DeadlineExceeded} {
		script := &scriptedPostgresScript{}
		store, cleanup := scriptedPostgresStore(t, script)
		work := secretaryDriverWork()
		require.NoError(t, store.FailSecretaryJob(t.Context(), work, failure))
		require.Len(t, script.execTexts, 1)
		assert.Contains(t, script.execTexts[0], "WHERE source_id=$1 AND lease_token=$2")
		assert.Contains(t, script.execTexts[0], "auto_retry_count<$5")
		code := domain.SecretaryValidationFailureCode(failure)
		key := ""
		if code != "" {
			key = secretaryValidationRetryKey(work)
		}
		assert.Equal(t, []driver.NamedValue{
			{Ordinal: 1, Value: work.SourceID}, {Ordinal: 2, Value: work.LeaseToken},
			{Ordinal: 3, Value: failure.Error()}, {Ordinal: 4, Value: key},
			{
				Ordinal: 5,
				Value:   int64(domain.SecretaryMaxValidationRetries),
			}, {Ordinal: 6, Value: code},
		}, script.execArgs[0])
		cleanup()
	}
}

func TestSecretaryValidationRetryPostgresGivenExplicitRetryDuringActiveRunThenPreservesItsBudgetAndLease(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	old := secretaryClaim(t, store)
	require.NoError(t, store.FailSecretaryJob(t.Context(), old, validationFailure()))
	releaseSecretaryBackoff(t, store)
	current := secretaryClaim(t, store)
	require.NoError(t, store.RetrySecretaryJobs(t.Context(), "owner"))
	require.NoError(t, store.FailSecretaryJob(t.Context(), old, validationFailure()))
	generation, count, _ := readSecretaryRetryState(t, store, current.SourceID)
	assert.Equal(t, current.RetryGeneration, generation)
	assert.Equal(t, 1, count)
	active, err := store.SecretaryJobActive(t.Context(), current)
	require.NoError(t, err)
	assert.True(t, active)
}

func TestSecretaryValidationRetryPostgresGivenExistingQueueThenAdditiveMigrationPreservesWork(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	work := secretaryClaim(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`ALTER TABLE secretary_jobs DROP COLUMN auto_retry_key, DROP COLUMN auto_retry_count, DROP COLUMN last_validation_failure`,
	)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "secretary.sql"))
	require.NoError(t, err)
	for range 2 {
		_, err = store.db.ExecContext(t.Context(), string(raw))
		require.NoError(t, err)
	}
	generation, count, key := readSecretaryRetryState(t, store, work.SourceID)
	assert.Zero(t, generation)
	assert.Zero(t, count)
	assert.Empty(t, key)
	active, err := store.SecretaryJobActive(t.Context(), work)
	require.NoError(t, err)
	assert.True(t, active)
	var desired, processed int64
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT desired_revision,processed_revision FROM secretary_jobs WHERE source_id=$1`, work.SourceID).
			Scan(&desired, &processed),
	)
	assert.Equal(t, work.ThroughRevision, desired)
	assert.Zero(t, processed)
}

func TestSecretaryValidationRetryPostgresGivenExpiredLeaseThenCannotSpendBudgetUntilClaimedAgain(
	t *testing.T,
) {
	store := secretaryPostgresFixture(t)
	secretaryEnable(t, store)
	old := secretaryClaim(t, store)
	_, err := store.db.ExecContext(
		t.Context(),
		`UPDATE secretary_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE source_id=$1`,
		old.SourceID,
	)
	require.NoError(t, err)
	require.NoError(t, store.FailSecretaryJob(t.Context(), old, validationFailure()))
	generation, count, key := readSecretaryRetryState(t, store, old.SourceID)
	assert.Zero(t, generation)
	assert.Zero(t, count)
	assert.Empty(t, key)
	var failure string
	require.NoError(
		t,
		store.db.QueryRowContext(t.Context(), `SELECT last_error FROM secretary_jobs WHERE source_id=$1`, old.SourceID).
			Scan(&failure),
	)
	assert.Empty(t, failure)
	current := secretaryClaim(t, store)
	assert.NotEqual(t, old.LeaseToken, current.LeaseToken)
	require.NoError(t, store.FailSecretaryJob(t.Context(), current, validationFailure()))
	generation, count, _ = readSecretaryRetryState(t, store, current.SourceID)
	assert.EqualValues(t, 1, generation)
	assert.Equal(t, 1, count)
}

package storage

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryExecutionStatusGivenDurableStatesThenReportsOnlySafeEnums(t *testing.T) {
	for _, test := range []struct {
		batch, attempt, state, reason string
		leased                        bool
		pending                       int
	}{
		{"running", "running", "running", "", true, 1},
		{"running", "running", "needs_recovery", "lease_expired", false, 1},
		{"output_received", "output_received", "output_received", "", false, 1},
		{"unknown", "unknown", "unknown", "execution_unknown", false, 1},
		{"blocked", "invalid", "blocked", "invalid_output", false, 1},
		{"blocked", "failed", "blocked", "execution_failed", false, 1},
		{"blocked", "not_dispatched", "blocked", "connection_retries_exhausted", false, 1},
		{"blocked", "canceled", "blocked", "canceled", false, 1},
		{"blocked", "", "blocked", "retry_required", false, 1},
		{"pending", "invalid", "queued", "", true, 1},
		{"pending", "not_dispatched", "waiting", "connection_backoff", false, 1},
		{"", "", "waiting", "", false, 1},
		{"", "", "", "", false, 0},
	} {
		t.Run(test.state+"/"+test.reason+"/"+test.attempt, func(t *testing.T) {
			state, reason := secretaryExecutionStatus(
				test.batch,
				test.attempt,
				test.leased,
				test.pending,
			)
			assert.Equal(t, test.state, state)
			assert.Equal(t, test.reason, reason)
		})
	}
}

func TestSecretaryExecutionStatusDriverGivenLatestBatchThenScopesOwnerSettingsAndRetryEpoch(
	t *testing.T,
) {
	for _, test := range []struct {
		batch, attempt, state, reason string
		leased                        bool
	}{
		{"running", "running", "needs_recovery", "lease_expired", false},
		{"running", "running", "running", "", true},
		{"unknown", "unknown", "unknown", "execution_unknown", false},
		{"blocked", "invalid", "blocked", "invalid_output", false},
		{"blocked", "not_dispatched", "blocked", "connection_retries_exhausted", false},
		{"pending", "not_dispatched", "waiting", "connection_backoff", false},
		{"output_received", "output_received", "output_received", "", false},
	} {
		t.Run(test.state, func(t *testing.T) {
			script := &scriptedPostgresScript{queries: []scriptedRows{
				{
					columns: []string{"owner", "agent", "enabled", "revision", "updated"},
					values:  [][]driver.Value{{"owner", "secretary", true, int64(7), time.Now()}},
				},
				{
					columns: []string{
						"pending",
						"processing",
						"completed",
						"error",
						"batch",
						"attempt",
						"leased",
					},
					values: [][]driver.Value{
						{
							int64(1),
							test.leased,
							nil,
							"private raw failure",
							test.batch,
							test.attempt,
							test.leased,
						},
					},
				},
			}}
			store, cleanup := scriptedPostgresStore(t, script)
			defer cleanup()
			status, err := store.GetSecretaryStatus(t.Context(), "owner")
			require.NoError(t, err)
			assert.Equal(t, test.state, status.ExecutionState)
			assert.Equal(t, test.reason, status.BlockedReason)
			assert.Equal(t, "owner", script.queryArgs[1][0].Value)
			assert.Equal(t, int64(7), script.queryArgs[1][1].Value)
			assert.Contains(t, script.queryTexts[1], "b.retry_epoch=j.retry_epoch")
			assert.Contains(t, script.queryTexts[1], "b.settings_revision=$2")
		})
	}
}

func TestSecretaryExecutionStatusGivenConnectionFailureThenShowsDurableBackoff(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(t, f.store.FailSecretaryAttempt(
			t.Context(), job, job.AttemptID, domain.ErrSecretaryNotDispatched,
		))
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "waiting", status.ExecutionState)
		assert.Equal(t, "connection_backoff", status.BlockedReason)
		assert.False(t, status.Processing)
		for range domain.SecretaryConnectionMaxRetries {
			f.releaseConnectionBackoff(t)
			job = f.begin(t, f.prepare(t))
			require.NoError(t, f.store.FailSecretaryAttempt(
				t.Context(), job, job.AttemptID, domain.ErrSecretaryNotDispatched,
			))
		}
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "blocked", status.ExecutionState)
		assert.Equal(t, "connection_retries_exhausted", status.BlockedReason)
		assert.False(t, status.Processing)
	})
}

func TestSecretaryExecutionStatusMemoryGivenObsoleteOrForeignBatchThenDoesNotLeakIt(t *testing.T) {
	store, _ := secretaryMemoryFixture(t)
	settings := secretaryEnable(t, store)
	job := secretaryClaim(t, store)
	prepared, err := store.PrepareSecretaryBatch(t.Context(), job)
	require.NoError(t, err)
	attempt, err := store.BeginSecretaryAttempt(t.Context(), prepared)
	require.NoError(t, err)
	batch := store.secretaryState.executions[prepared.BatchID]
	for _, mutate := range []func(){
		func() { batch.owner = "foreign" },
		func() { batch.owner, batch.settings = "owner", settings.Revision+1 },
		func() { batch.settings, batch.epoch = settings.Revision, 99 },
		func() { batch.epoch, batch.state = 0, "committed" },
		func() { batch.state = "superseded" },
	} {
		mutate()
		status, statusErr := store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, statusErr)
		assert.Equal(t, "waiting", status.ExecutionState)
		assert.Empty(t, status.BlockedReason)
	}
	batch.state = "running"
	batch.attempts[0].ID = attempt.ID
	status, err := store.GetSecretaryStatus(t.Context(), "foreign")
	require.NoError(t, err)
	assert.Empty(t, status.ExecutionState)
	assert.Zero(t, status.PendingCount)
}

func TestSecretaryExecutionStatusGivenLifecycleThenDistinguishesQueuedRunningAndSavedOutput(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "waiting", status.ExecutionState)
		job := f.prepare(t)
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "queued", status.ExecutionState)
		job = f.begin(t, job)
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "running", status.ExecutionState)
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"saved output",
				domain.SecretaryToolReads{},
			),
		)
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "output_received", status.ExecutionState)
		_, err = f.store.PublishSecretaryBatch(
			t.Context(),
			job,
			secretaryNodesPublication(job, "decision"),
		)
		require.NoError(t, err)
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Empty(t, status.ExecutionState)
	})
}

func TestSecretaryExecutionStatusGivenExpiredLeaseOrUnknownNativeThenDoesNotClaimToBeRunning(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		f.begin(t, f.prepare(t))
		f.expire(t)
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "needs_recovery", status.ExecutionState)
		assert.Equal(t, "lease_expired", status.BlockedReason)
		assert.False(t, status.Processing)
		job := f.prepare(t)
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(
				t.Context(),
				job,
				job.AttemptID,
				domain.ErrSecretaryExecutionUnknown,
			),
		)
		status, err = f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "unknown", status.ExecutionState)
		assert.Equal(t, "execution_unknown", status.BlockedReason)
	})
}

func TestSecretaryExecutionStatusGivenExhaustedValidationThenShowsFixedReason(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.prepare(t)
		for range 3 {
			job = f.begin(t, job)
			require.NoError(
				t,
				f.store.FailSecretaryAttempt(
					t.Context(),
					job,
					job.AttemptID,
					domain.SecretaryOutputValidationError{
						Code: "schema",
						Err:  errors.New("private source text"),
					},
				),
			)
		}
		status, err := f.store.GetSecretaryStatus(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, "blocked", status.ExecutionState)
		assert.Equal(t, "invalid_output", status.BlockedReason)
		assert.False(t, strings.Contains(status.BlockedReason, "private"))
	})
}

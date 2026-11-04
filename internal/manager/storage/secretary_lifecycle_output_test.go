package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryLifecycleGivenPersistedOutputThenUnknownDeliveryDoesNotDiscardIt(t *testing.T) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		job := f.begin(t, f.prepare(t))
		require.NoError(
			t,
			f.store.SaveSecretaryAttemptOutput(
				t.Context(),
				job,
				job.AttemptID,
				"stored output",
				domain.SecretaryToolReads{},
			),
		)
		require.NoError(
			t,
			f.store.FailSecretaryAttempt(
				t.Context(),
				job,
				job.AttemptID,
				domain.ErrSecretaryExecutionUnknown,
			),
		)
		require.NoError(
			t,
			f.store.FailSecretaryJob(t.Context(), job, domain.ErrSecretaryExecutionUnknown),
		)
		f.releaseBackoff(t)
		f.restart()
		restored := f.prepare(t)
		require.NotNil(t, restored.Attempt)
		assert.Equal(t, "output_received", restored.Attempt.State)
		assert.Equal(t, "stored output", restored.Attempt.Output)
		assert.Equal(t, job.AttemptID, restored.Attempt.ID)
		assert.Equal(t, 1, f.attemptCount(t, job.BatchID))
	})
}

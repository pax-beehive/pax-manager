package storage

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func TestSecretaryValidationFeedbackGivenRejectedOutputThenPreparedRetryUsesThePersistedSafeCorrection(
	t *testing.T,
) {
	for _, scenario := range []struct {
		category, failure, detail, policy string
	}{
		{"evidence", "secretary evidence was not present in the supplied or verified text", "quote_not_visible", domain.SecretaryLegacyOutputPolicyVersion},
		{"schema", "secretary batch publication exceeds 64 KiB after source binding", "source_binding_capacity", domain.SecretaryLegacyOutputPolicyVersion},
		{"evidence", "secretary evidence was not present in the supplied or verified text", "source_unavailable", domain.SecretarySourceOnlyOutputPolicyVersion},
		{"schema", "secretary batch publication exceeds 64 KiB after source binding", "source_binding_capacity", domain.SecretarySourceOnlyOutputPolicyVersion},
	} {
		t.Run(scenario.category+"/"+scenario.policy, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				work := secretaryClaim(t, f.store)
				work.OutputPolicyVersion = scenario.policy
				prepared, err := f.store.PrepareSecretaryBatch(t.Context(), work)
				require.NoError(t, err)
				job := f.begin(t, prepared)
				failure := domain.SecretaryOutputValidationError{
					Code: scenario.category, Err: errors.New(scenario.failure),
				}
				require.NoError(
					t,
					f.store.FailSecretaryAttempt(t.Context(), job, job.AttemptID, failure),
				)
				require.NoError(t, f.store.FailSecretaryJob(t.Context(), job, failure))
				f.releaseBackoff(t)
				restored := f.prepare(t)
				assert.Equal(t, scenario.policy, restored.OutputPolicyVersion)
				require.NotNil(t, restored.Attempt)
				assert.Equal(t, "invalid", restored.Attempt.State)
				assert.Equal(t, job.AttemptID, restored.Attempt.ID)
				assert.Equal(t, job.BatchID, restored.Attempt.BatchID)
				assert.Equal(t, scenario.failure, restored.Attempt.Error)
				assert.Equal(t, scenario.category, restored.ValidationFeedback)
				prompt, err := secretary.BuildNodePrompt(restored)
				require.NoError(t, err)
				assert.Contains(t, prompt, `"previous_validation_failure":"`+scenario.category+`"`)
				assert.Contains(t, prompt, `"validation_feedback":{"code":"`+scenario.detail+`"`)
				assert.NotContains(t, prompt, scenario.failure)
				assert.Equal(
					t,
					1,
					f.attemptCount(t, job.BatchID),
					"building a retry prompt must not dispatch another attempt",
				)
			})
		})
	}
}

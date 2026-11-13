package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func secretaryOutputPolicyIdentityJob() domain.SecretaryJob {
	return domain.SecretaryJob{
		OwnerUserID: "owner", SourceID: "1", SourceAgentID: "source-agent",
		SourceSessionID: "source-session", AgentID: "secretary", SettingsRevision: 2,
		RetryEpoch: 3, RetryGeneration: 5, ExpectedRevision: 7, ThroughRevision: 10,
		Messages: []domain.SearchDocument{{
			SourceID: "m", ContentHash: "sha256:body", CanonicalizationVersion: "visible-v1",
		}},
	}
}

func secretaryLegacyNodesReadyJob(t *testing.T, store interface {
	domain.SecretaryStore
	domain.SecretaryExecutionStore
}) domain.SecretaryJob {
	t.Helper()
	work := secretaryClaim(t, store)
	work.OutputPolicyVersion = domain.SecretaryLegacyOutputPolicyVersion
	return secretaryNodesReadyWork(t, store, work)
}

func TestSecretaryOutputPolicyGivenEmptyLegacyVersionThenKeepsHistoricalIdentities(t *testing.T) {
	legacy := secretaryOutputPolicyIdentityJob()
	assert.Equal(t, "batch_6ae8d605979d9d44041b16f1648190d7", secretaryBatchID(legacy))
	assert.Equal(
		t,
		"secretary:615ea6fb58e0703f9afb32df37bab765e43eaafd865c5aa9c1451923b9878d20",
		secretaryRunID(legacy),
	)
	current := legacy
	current.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	assert.NotEqual(t, secretaryBatchID(legacy), secretaryBatchID(current))
	assert.NotEqual(t, secretaryRunID(legacy), secretaryRunID(current))
	assert.NotEqual(t, secretaryValidationRetryKey(legacy), secretaryValidationRetryKey(current))
	assert.Equal(t, "", legacy.OutputPolicyVersion)
}

func TestSecretaryOutputPolicyGivenFreshJobThenDefaultsOnlyItsOutputContract(t *testing.T) {
	for _, policy := range []string{"", domain.SecretaryLegacyOutputPolicyVersion, domain.SecretarySourceOnlyOutputPolicyVersion} {
		t.Run(policy, func(t *testing.T) {
			job := secretaryOutputPolicyIdentityJob()
			job.OutputPolicyVersion = policy
			before, err := json.Marshal(job.Messages)
			require.NoError(t, err)
			secretaryPrepareJob(&job)
			want := policy
			if want == "" {
				want = domain.SecretarySourceOnlyOutputPolicyVersion
			}
			assert.Equal(t, want, job.OutputPolicyVersion)
			assert.Equal(t, "owner", job.OwnerUserID)
			assert.Equal(t, "source-agent", job.SourceAgentID)
			assert.Equal(t, "source-session", job.SourceSessionID)
			after, err := json.Marshal(job.Messages)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after))
		})
	}
}

func TestSecretaryOutputPolicyGivenFrozenBatchThenNeverUpgradesItsOriginalContract(t *testing.T) {
	for _, policy := range []string{"", domain.SecretaryLegacyOutputPolicyVersion, domain.SecretarySourceOnlyOutputPolicyVersion} {
		t.Run(policy, func(t *testing.T) {
			original := secretaryOutputPolicyIdentityJob()
			original.OutputPolicyVersion = policy
			original.BatchID = secretaryBatchID(original)
			raw := secretaryBatchInput(original)
			current := original
			current.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
			current.LeaseToken, current.RetryGeneration = "current-lease", 6
			restored, err := secretaryRestoreBatch(raw, current)
			require.NoError(t, err)
			assert.Equal(t, policy, restored.OutputPolicyVersion)
			assert.Equal(t, original.BatchID, secretaryBatchID(restored))
			assert.Equal(t, original.Messages, restored.Messages)
			assert.Equal(t, original.OwnerUserID, restored.OwnerUserID)
			assert.Equal(t, "current-lease", restored.LeaseToken)
			assert.EqualValues(t, 6, restored.RetryGeneration)
			require.NoError(t, validateSecretaryBatchIdentity(restored, current))
		})
	}
	unsupported := secretaryOutputPolicyIdentityJob()
	unsupported.OutputPolicyVersion = "future-output-policy"
	_, err := secretaryRestoreBatch(secretaryBatchInput(unsupported), unsupported)
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	assert.ErrorIs(
		t,
		validateSecretaryBatchIdentity(unsupported, unsupported),
		domain.ErrSecretaryFenced,
	)
}

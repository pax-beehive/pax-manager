package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretaryOutputPolicyGivenLegacyOrCurrentVersionThenSelectsFrozenContract(
	t *testing.T,
) {
	for _, scenario := range []struct {
		policy     string
		supported  bool
		sourceOnly bool
	}{
		{"", true, false},
		{SecretaryLegacyOutputPolicyVersion, true, false},
		{SecretarySourceOnlyOutputPolicyVersion, true, true},
		{"future-policy", false, false},
	} {
		t.Run(scenario.policy, func(t *testing.T) {
			assert.Equal(t, scenario.supported, SecretaryOutputPolicySupported(scenario.policy))
			assert.Equal(t, scenario.sourceOnly, SecretarySourceOnlyOutput(scenario.policy))
		})
	}
}

func TestSecretaryOutputPolicyGivenLegacyJobThenKeepsItsPersistedJSONShape(t *testing.T) {
	legacy, err := json.Marshal(SecretaryJob{SourceID: "1"})
	require.NoError(t, err)
	assert.NotContains(t, string(legacy), "OutputPolicyVersion")
	current, err := json.Marshal(SecretaryJob{
		SourceID: "1", OutputPolicyVersion: SecretarySourceOnlyOutputPolicyVersion,
	})
	require.NoError(t, err)
	assert.Contains(t, string(current), `"OutputPolicyVersion":"summary-sources-v2"`)
}

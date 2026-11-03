package secretary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryValidationRetryGivenRejectedGeneratedOutputThenCarriesTypedFeedback(
	t *testing.T,
) {
	for _, tc := range []struct{ output, code string }{
		{"not JSON", "schema"},
		{`{"body":""}`, "schema"},
		{`{"body":"valid","claims":[{"text":"claim","evidence":[{"message_id":"invented","quote":"missing"}]}]}`, "evidence"},
		{`{"body":"valid","claims":[{"text":"claim","evidence":[{"message_id":"message-1","quote":"not present"}]}]}`, "evidence"},
		{`{"body":"` + strings.Repeat("x", maxSummaryStateBytes) + `"}`, "schema"},
	} {
		_, err := BuildPublication(testJob(), tc.output)
		require.Error(t, err)
		assert.Equal(t, tc.code, domain.SecretaryValidationFailureCode(err))
	}
	_, err := RequestedSourceReads(
		testJob(),
		`{"body":"valid","claims":[{"text":"claim","evidence":[{"message_id":"a","quote":"b","message":{"message_id":"conflict"},"read_receipt":"proof"}]}]}`,
	)
	require.Error(t, err)
	assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))
}

func TestSecretaryValidationRetryGivenInvalidInputOrBaselineThenDoesNotRequestFreshGeneration(
	t *testing.T,
) {
	job := testJob()
	job.Messages[0].OwnerUserID = "foreign"
	_, err := BuildPublication(job, `{"body":"updated"}`)
	require.Error(t, err)
	assert.Empty(t, domain.SecretaryValidationFailureCode(err))
	job = rollingTestJob()
	job.CurrentSummary.OwnerUserID = "foreign"
	_, err = BuildPublication(job, `{"body":"updated"}`)
	require.Error(t, err)
	assert.Empty(t, domain.SecretaryValidationFailureCode(err))
}

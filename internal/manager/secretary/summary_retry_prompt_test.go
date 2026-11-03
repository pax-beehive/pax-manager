package secretary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryRetryPromptGivenChangedOrRemovedEvidenceThenDoesNotOfferOldQuotesAsReusable(
	t *testing.T,
) {
	job := rollingTestJob()
	job.CurrentSummary.Claims = append(
		job.CurrentSummary.Claims,
		domain.SummaryClaim{
			ID:   "changed",
			Text: "Earlier changed decision",
			Evidence: []domain.SummaryEvidence{
				{
					Message: messageReference(job.Messages[0]),
					Quote:   "obsolete quote outside the current last view",
				},
				{
					Message: domain.MessageVersionRef{MessageID: "removed"},
					Quote:   "removed old quote",
				},
			},
		},
	)
	job.RemovedMessageIDs = []string{"removed"}
	job.ValidationFeedback = "evidence"
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Earlier changed decision")
	assert.Contains(t, prompt, "Earlier decision")
	assert.NotContains(t, prompt, "obsolete quote outside the current last view")
	assert.NotContains(t, prompt, "removed old quote")
	assert.Contains(t, prompt, `"previous_validation_failure":"evidence"`)
	assert.Contains(t, prompt, "Never invent message IDs")
	job.ValidationFeedback = strings.Repeat("untrusted raw failure", 100)
	prompt, err = BuildPrompt(job)
	require.NoError(t, err)
	assert.NotContains(t, prompt, "untrusted raw failure")
}

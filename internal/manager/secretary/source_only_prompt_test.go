package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodePromptGivenSourceOnlyPolicyThenProjectsContentAndSourcesWithoutLegacyClaims(
	t *testing.T,
) {
	job := rollingTestJob()
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	job.CurrentSummaries = []domain.SummaryVersion{*job.CurrentSummary}
	job.Instructions = guidanceTestSnapshot(job)
	job.SummariesCursor, job.SummariesHasMore = "next-page", true
	job.CurrentSummaries[0].Claims[0].Evidence[0].Quote = strings.Repeat("legacy-quote", 10000)
	prompt, err := BuildNodePrompt(job)
	require.NoError(t, err)
	var payload struct {
		Summaries    []map[string]json.RawMessage         `json:"current_summaries"`
		Instructions *domain.SecretaryInstructionSnapshot `json:"owner_instructions"`
		Cursor       string                               `json:"summaries_cursor"`
	}
	require.NoError(
		t,
		json.Unmarshal(
			[]byte(strings.TrimPrefix(prompt, sourceOnlyNodeSummaryInstructions)),
			&payload,
		),
	)
	require.Len(t, payload.Summaries, 1)
	assert.NotContains(t, payload.Summaries[0], "claims")
	assert.NotContains(t, prompt, "legacy-quote")
	assert.Equal(t, job.Instructions, payload.Instructions)
	assert.Equal(t, "next-page", payload.Cursor)
	assert.JSONEq(t, `["old-message"]`, string(payload.Summaries[0]["source_message_ids"]))
	assert.JSONEq(t, `"summary-target"`, string(payload.Summaries[0]["summary_id"]))
	assert.JSONEq(t, `"`+job.CurrentSummary.Body+`"`, string(payload.Summaries[0]["body"]))
	for _, required := range []string{"source_message_ids", "related_sources", "read_receipt", "expected_revision", "one outcome", "64 KiB", "workspace_summaries", "untrusted data", "run takes precedence over session, then user"} {
		assert.Contains(t, prompt, required)
	}
	assert.NotContains(t, sourceOnlyNodeSummaryInstructions, `"claims"`)
	assert.NotContains(t, sourceOnlyNodeSummaryInstructions, `"evidence"`)
	assert.NotContains(t, sourceOnlyNodeSummaryInstructions, `"quote"`)
	assert.Equal(t, 120000, len(job.CurrentSummaries[0].Claims[0].Evidence[0].Quote))
}

func TestNodePromptGivenSourceOnlyPolicyThenPreservesSelectiveMessageViews(t *testing.T) {
	job := longUserPolicyJob(domain.SecretaryInputPolicyVersion)
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	prompt, err := BuildNodePrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Opening request.")
	assert.Contains(t, prompt, "Closing question.")
	assert.Contains(t, prompt, `"view":"head_tail"`)
	assert.NotContains(t, prompt, "Hidden source conclusion.")
	assert.Contains(t, prompt, job.Messages[0].ContentHash)
	job = paragraphJob()
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	prompt, err = BuildNodePrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Final decision.")
	assert.NotContains(t, prompt, "Earlier detailed explanation.")
	assert.Contains(t, prompt, `"view":"last"`)
}

func TestSummaryPromptProjectionGivenSourceOnlyPolicyThenNeverCopiesClaims(t *testing.T) {
	job := rollingTestJob()
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	state := &summaryPromptState{
		SummaryID: "summary",
		Body:      "Retained content",
		Claims:    job.CurrentSummary.Claims,
	}
	projected := promptStateForJob(state, job)
	require.NotNil(t, projected)
	assert.Equal(t, state.Body, projected.Body)
	assert.Empty(t, projected.Claims)
	assert.NotEmpty(t, state.Claims)
	assert.Nil(t, promptStateForJob(nil, job))
	job.OutputPolicyVersion = ""
	assert.NotEmpty(t, promptStateForJob(state, job).Claims)
}

func TestRollingPromptGivenSourceOnlyPolicyThenOmitsLegacyClaimsBeforeBudgeting(t *testing.T) {
	job := rollingTestJob()
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	job.CurrentSummary.Claims[0].Evidence[0].Quote = strings.Repeat("legacy-quote", 10000)
	prompt, err := BuildPrompt(job)
	require.NoError(t, err)
	var payload struct {
		Summary map[string]json.RawMessage `json:"current_summary"`
	}
	require.NoError(
		t,
		json.Unmarshal(
			[]byte(strings.TrimPrefix(prompt, sourceOnlyRollingSummaryInstructions)),
			&payload,
		),
	)
	assert.NotContains(t, payload.Summary, "claims")
	assert.NotContains(t, prompt, "legacy-quote")
	assert.NotContains(t, sourceOnlyRollingSummaryInstructions, `"claims"`)
	assert.NotContains(t, sourceOnlyRollingSummaryInstructions, `"evidence"`)
	assert.NotContains(t, sourceOnlyRollingSummaryInstructions, `"quote"`)
	assert.Contains(t, prompt, "related_sources")
	assert.Contains(t, prompt, "current permission")
	assert.Equal(t, 120000, len(job.CurrentSummary.Claims[0].Evidence[0].Quote))
}

func TestNodeFeedbackGivenSourceOnlyPolicyThenOnlyRequestsSourceOrSchemaCorrection(t *testing.T) {
	for _, scenario := range []struct{ category, failure, code string }{
		{"schema", "secretary batch publication exceeds 64 KiB after source binding", "source_binding_capacity"},
		{"evidence", "secretary evidence was not present in the supplied or verified text", "source_unavailable"},
		{"evidence", "secretary evidence quote is empty", "source_unavailable"},
		{"evidence", "summary selected an unavailable source message", "source_unavailable"},
		{"evidence", "private unknown failure", "evidence"},
		{"schema", "private unknown failure", "schema"},
	} {
		t.Run(scenario.failure, func(t *testing.T) {
			job := validationFeedbackJob(scenario.category, scenario.failure)
			job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
			feedback := nodeValidationFeedback(job)
			require.NotNil(t, feedback)
			assert.Equal(t, scenario.code, feedback.Code)
			assert.NotContains(t, feedback.Hint, "quote")
			assert.NotContains(t, feedback.Hint, "claim")
			assert.NotContains(t, feedback.Hint, "private")
		})
	}
	job := testJob()
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	assert.Nil(t, nodeValidationFeedback(job))
}

func TestSecretaryPromptGivenUnknownOutputPolicyThenRejectsInsteadOfReinterpreting(t *testing.T) {
	job := testJob()
	job.OutputPolicyVersion = "future-output-policy"
	for _, build := range []func(domain.SecretaryJob) (string, error){BuildNodePrompt, BuildPrompt} {
		_, err := build(job)
		assert.Error(t, err)
	}
}

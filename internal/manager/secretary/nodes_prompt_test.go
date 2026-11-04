package secretary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodePromptGivenMultipleSummariesThenExposesBoundedCurrentStateAndExplicitPagination(
	t *testing.T,
) {
	job := rollingTestJob()
	job.BatchID = "batch"
	job.CurrentSummaries = []domain.SummaryVersion{*job.CurrentSummary}
	second := *job.CurrentSummary
	second.SummaryID = "another-summary"
	second.Title = "Deployment"
	job.CurrentSummaries = append(job.CurrentSummaries, second)
	job.SummariesCursor = "next-page"
	prompt, err := BuildNodePrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, `"current_summaries"`)
	assert.Contains(t, prompt, `"summary_id":"summary-target"`)
	assert.Contains(t, prompt, `"summary_id":"another-summary"`)
	assert.Contains(t, prompt, `"source_message_ids":["old-message"]`)
	assert.Contains(t, prompt, `"summaries_cursor":"next-page"`)
	assert.Contains(t, prompt, "workspace_summaries")
	assert.Contains(t, prompt, "no_summary")
	assert.Contains(t, prompt, "tool calls, tool results, and reasoning")
	assert.Contains(t, prompt, "untrusted data")
	assert.NotContains(t, prompt, "maintaining one current summary")
}

func TestNodePromptGivenForeignDuplicateOrOversizedStateThenRejects(t *testing.T) {
	for name, mutate := range map[string]func(*domain.SecretaryJob){
		"foreign":          func(job *domain.SecretaryJob) { job.CurrentSummaries[0].OwnerUserID = "other" },
		"invalid revision": func(job *domain.SecretaryJob) { job.CurrentSummaries[0].Revision = 0 },
		"duplicate": func(job *domain.SecretaryJob) {
			job.CurrentSummaries = append(job.CurrentSummaries, job.CurrentSummaries[0])
		},
		"oversized": func(job *domain.SecretaryJob) {
			job.CurrentSummaries[0].Body = strings.Repeat("x", maxSummaryStateBytes+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			job := rollingTestJob()
			job.CurrentSummaries = []domain.SummaryVersion{*job.CurrentSummary}
			mutate(&job)
			_, err := BuildNodePrompt(job)
			assert.Error(t, err)
		})
	}
}

func TestNodePromptGivenAssistantParagraphsThenOnlyExposesLastVisibleParagraph(t *testing.T) {
	job := testJob()
	job.Messages[0].Role = "assistant"
	job.Messages[0].Body = "Earlier detail.\n\nFinal decision."
	job.Messages[0].ContentHash = domain.SearchContentHash(job.Messages[0].Body)
	prompt, err := BuildNodePrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Final decision.")
	assert.NotContains(t, prompt, "Earlier detail.")
	assert.Contains(t, prompt, `"view":"last"`)
}

func TestNodePromptGivenLargeLegacySummaryOutsideContextThenExplicitlyStartsDiscoveryFromEmptyCursor(
	t *testing.T,
) {
	job := testJob()
	job.SummariesHasMore = true
	prompt, err := BuildNodePrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, `"summaries_has_more":true`)
	assert.Contains(t, prompt, `"current_summaries":[]`)
	assert.NotContains(t, prompt, `"summaries_cursor"`)
	assert.Contains(t, prompt, "An empty cursor means start from the first page")
	assert.Contains(t, prompt, "requires_read:true")
	assert.Contains(t, prompt, "Do not repeatedly list the same header")
}

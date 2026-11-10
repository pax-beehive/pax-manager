package secretary

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func retainedSubquoteNode(job domain.SecretaryJob, quote string) generatedNodeSummary {
	baseline := job.CurrentSummaries[0]
	return generatedNodeSummary{
		Action: "revise", SummaryID: baseline.SummaryID, ExpectedRevision: &baseline.Revision,
		Title: "Decision", Body: "Keep the earlier decision",
		SourceMessageIDs: []string{"old-message"},
		Claims: []generatedClaim{{Text: "Earlier decision", Evidence: []generatedEvidence{{
			MessageID: "old-message", Quote: quote,
		}}}},
	}
}

func TestNodePublicationGivenLiteralBaselineSubquoteThenRetainsExactVersionAndView(t *testing.T) {
	for _, quote := range []string{"Earlier decision", "Earlier", "decision", "arlier dec"} {
		t.Run(quote, func(t *testing.T) {
			job := nodeTestJob()
			original := job.CurrentSummaries[0].Claims[0].Evidence[0]
			prompt, err := BuildNodePrompt(job)
			require.NoError(t, err)
			assert.Contains(t, prompt, `"quote":"Earlier decision"`)
			publication, err := BuildNodePublication(job,
				nodeBatchOutput(
					t,
					[]generatedNodeSummary{retainedSubquoteNode(job, quote)},
					[]generatedNodeOutcome{
						{MessageID: "message-1", Action: "no_summary", Reason: "No new conclusion"},
					},
				),
				domain.SecretaryToolReads{})
			require.NoError(t, err)
			actual := publication.Outputs[0].Request.Claims[0].Evidence[0]
			assert.Equal(t, quote, actual.Quote)
			assert.Equal(t, original.Message, actual.Message)
			assert.Equal(t, original.View, actual.View)
			assert.Equal(t, original.ReadReceipt, actual.ReadReceipt)
			assert.Equal(t, original, job.CurrentSummaries[0].Claims[0].Evidence[0])
		})
	}
}

func TestRetainedEvidenceGivenUntrustedOrInvalidatedSubquoteThenRejects(t *testing.T) {
	for _, scenario := range []string{
		"changed", "removed", "reset", "no_baseline", "other_node", "other_message",
		"empty", "paraphrase", "joined_fragments", "wrong_version", "wrong_view",
		"wrong_receipt", "wrong_nested_receipt",
	} {
		t.Run(scenario, func(t *testing.T) {
			job := rollingTestJob()
			evidence := generatedEvidence{MessageID: "old-message", Quote: "Earlier"}
			switch scenario {
			case "changed":
				changed := job.Messages[0]
				changed.SourceID, changed.Body = "old-message", "Replacement fact"
				changed.ContentHash = domain.SearchContentHash(changed.Body)
				job.Messages = append(job.Messages, changed)
			case "removed":
				job.RemovedMessageIDs = []string{"old-message"}
			case "reset":
				job.ResetRequired = true
			case "no_baseline":
				job.CurrentSummary = nil
			case "other_node":
				job.CurrentSummaries = []domain.SummaryVersion{*job.CurrentSummary}
				job.CurrentSummary = &domain.SummaryVersion{SummaryID: "other-node", Revision: 1}
			case "other_message":
				evidence.MessageID = "other-message"
			case "empty":
				evidence.Quote = ""
			case "paraphrase":
				evidence.Quote = "Earlier choice"
			case "joined_fragments":
				evidence.Quote = "Earlierdecision"
			case "wrong_version", "wrong_nested_receipt":
				ref := job.CurrentSummary.Claims[0].Evidence[0].Message
				if scenario == "wrong_version" {
					ref.ContentHash = domain.SearchContentHash("different")
				} else {
					ref.ReadReceipt = "unverified"
				}
				evidence.Message = &ref
			case "wrong_view":
				evidence.View = "last"
			case "wrong_receipt":
				evidence.ReadReceipt = "unverified"
			}
			_, err := bindRollingEvidence(evidence, job, nil, nil)
			assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))
		})
	}
}

func TestNodePublicationGivenSubquoteOutsideSelectedCoverageThenRejects(t *testing.T) {
	job := nodeTestJob()
	summary := retainedSubquoteNode(job, "Earlier")
	summary.SourceMessageIDs = []string{"message-1"}
	_, err := BuildNodePublication(job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome()),
		domain.SecretaryToolReads{})
	assert.ErrorContains(t, err, "selected summary coverage")
}

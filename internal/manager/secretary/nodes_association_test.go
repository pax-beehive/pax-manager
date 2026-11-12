package secretary

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodePublicationGivenRecurringWorkWithNewCrossSessionReleaseEvidenceThenRevisesCurrentSummary(
	t *testing.T,
) {
	job := nodeTestJob()
	baseline := &job.CurrentSummaries[0]
	baseline.Title = "Manual release workflow"
	baseline.Body = "Repeated manual deployments."
	baseline.Claims = nil
	baseline.Coverage[0].ContentHash = domain.SearchContentHash("Repeated manual deployments.")
	message := &job.Messages[0]
	message.Role = "assistant"
	message.Body = "Manual deployment completed: pax-manager PR #246, commit abc123def456."
	message.ContentHash = domain.SearchContentHash(message.Body)
	relatedText := "pax-manager PR #246 fixes summary selection; merged as commit abc123def456."
	related := domain.MessageVersionRef{
		MessageID:               "implementation-session-message",
		ContentHash:             domain.SearchContentHash(relatedText),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		ReadReceipt:             "authorized-original-read",
	}
	read := domain.SecretaryReadMessage{
		Ref: related, Text: relatedText, View: "full", ReadReceipt: related.ReadReceipt,
	}
	summary := generatedNodeSummary{
		Action:           "revise",
		SummaryID:        baseline.SummaryID,
		ExpectedRevision: &baseline.Revision,
		Title:            baseline.Title,
		Body:             "Repeated manual deployments. PR #246, the summary selection fix, was deployed at commit abc123def456.",
		SourceMessageIDs: []string{baseline.Coverage[0].MessageID, message.SourceID},
		RelatedSources:   []domain.MessageVersionRef{related},
		Claims: []generatedClaim{{
			Text: "The summary selection fix in pax-manager PR #246 was deployed at commit abc123def456.",
			Kind: "result", Basis: "agent_reported", State: "active",
			Evidence: []generatedEvidence{
				{MessageID: message.SourceID, Quote: message.Body},
				{
					MessageID:   related.MessageID,
					Quote:       relatedText,
					View:        read.View,
					ReadReceipt: read.ReadReceipt,
				},
			},
		}},
	}
	outcomes := []generatedNodeOutcome{{
		MessageID: message.SourceID, Action: "summarized", SummaryKeys: []string{baseline.SummaryID},
	}}
	output := nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes)
	reads := domain.SecretaryToolReads{Messages: []domain.SecretaryReadMessage{read}}
	updatedJob, publication, err := buildNodeResult(
		job,
		ExecutionResult{Text: output, Reads: reads},
	)
	require.NoError(t, err)
	require.Len(t, publication.Outputs, 1)
	request := publication.Outputs[0].Request
	assert.Equal(t, baseline.SummaryID, request.SummaryID)
	assert.Equal(t, baseline.Revision, request.ExpectedRevision)
	assert.Equal(t, "secretary:session:"+job.SourceID, request.SubjectID)
	assert.Equal(t, summary.Body, request.Body)
	assert.Equal(t, []domain.MessageVersionRef{
		baseline.Coverage[0], messageReference(*message), related,
	}, request.Coverage)
	require.Len(t, request.Claims, 1)
	require.Len(t, request.Claims[0].Evidence, 2)
	assert.Equal(t, related, request.Claims[0].Evidence[1].Message)
	assert.Equal(t, read.ReadReceipt, request.Claims[0].Evidence[1].ReadReceipt)
	assert.Equal(t, []domain.MessageVersionRef{related}, updatedJob.AllowedRelatedMessages)
	assert.Equal(t, job.SourceID, updatedJob.SourceID)
	assert.Equal(t, job.SourceSessionID, updatedJob.SourceSessionID)
	require.Len(t, publication.Outcomes, 1)
	assert.Equal(t, message.SourceID, publication.Outcomes[0].MessageID)
	assert.Equal(t, "summarized", publication.Outcomes[0].Action)
	assert.Equal(t, []string{baseline.SummaryID}, publication.Outcomes[0].SummaryKeys)

	t.Run("additional original never receives a processing outcome", func(t *testing.T) {
		foreignOutcomes := append(
			append([]generatedNodeOutcome(nil), outcomes...),
			generatedNodeOutcome{
				MessageID: related.MessageID, Action: "summarized", SummaryKeys: []string{baseline.SummaryID},
			},
		)
		_, err := BuildNodePublication(
			job,
			nodeBatchOutput(t, []generatedNodeSummary{summary}, foreignOutcomes),
			reads,
		)
		assert.ErrorContains(t, err, "each input requires exactly one processing outcome")
	})

	t.Run("search navigation without an original read is insufficient", func(t *testing.T) {
		_, err := BuildNodePublication(job, output, domain.SecretaryToolReads{})
		assert.ErrorContains(t, err, "verified read")
	})

	for name, mutate := range map[string]func(*domain.SecretaryReadMessage){
		"different message": func(candidate *domain.SecretaryReadMessage) { candidate.Ref.MessageID = "other" },
		"different version": func(candidate *domain.SecretaryReadMessage) { candidate.Ref.ContentHash = "other" },
		"different canonicalization": func(candidate *domain.SecretaryReadMessage) {
			candidate.Ref.CanonicalizationVersion = "other"
		},
		"different receipt": func(candidate *domain.SecretaryReadMessage) { candidate.ReadReceipt = "other" },
		"different view":    func(candidate *domain.SecretaryReadMessage) { candidate.View = "last" },
		"unseen quote":      func(candidate *domain.SecretaryReadMessage) { candidate.Text = "Other visible text." },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := read
			mutate(&candidate)
			_, err := BuildNodePublication(job, output, domain.SecretaryToolReads{
				Messages: []domain.SecretaryReadMessage{candidate},
			})
			assert.Error(t, err)
		})
	}
}

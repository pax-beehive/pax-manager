package secretary

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodePublicationGivenDeletedOnlySourceThenPublishesNeutralRevisionWithoutReadingOldBody(
	t *testing.T,
) {
	job := nodeTestJob()
	job.Messages = nil
	job.RemovedMessageIDs = []string{"old-message"}
	job.ResetRequired = true
	job.CurrentSummaries = nil
	job.InvalidatedSummaries = []domain.SummaryRef{{SummaryID: "invalidated", Revision: 3}}
	revision := int64(3)
	summary := generatedNodeSummary{
		Action:           "revise",
		SummaryID:        "invalidated",
		ExpectedRevision: &revision,
		Title:            "Unavailable summary",
		Body:             domain.SecretaryEmptySummaryBody,
	}
	outcomes := []generatedNodeOutcome{
		{MessageID: "old-message", Action: "summarized", SummaryKeys: []string{"invalidated"}},
	}
	publication, err := BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes),
		domain.SecretaryToolReads{},
	)
	require.NoError(t, err)
	require.Len(t, publication.Outputs, 1)
	assert.Empty(t, publication.Outputs[0].Request.Coverage)
	assert.Equal(t, domain.SecretaryEmptySummaryBody, publication.Outputs[0].Request.Body)
	summary.Title = "Old sensitive title"
	_, err = BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "fixed neutral")
}

func TestNodePublicationGivenInvalidatedTargetThenCannotSkipItsRepair(t *testing.T) {
	job := nodeTestJob()
	job.InvalidatedSummaries = []domain.SummaryRef{{SummaryID: "missing-repair", Revision: 3}}
	_, err := BuildNodePublication(
		job,
		nodeBatchOutput(
			t,
			nil,
			[]generatedNodeOutcome{
				{MessageID: "message-1", Action: "no_summary", Reason: "No new conclusions"},
			},
		),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "explicitly revised")
}

func TestNodePublicationGivenRemovedOrUncoveredInputThenCannotClaimSuccessfulSummary(t *testing.T) {
	job := nodeTestJob()
	summary := newNodeProposal()
	summary.SourceMessageIDs = []string{"old-message"}
	revision := int64(1)
	summary.Action, summary.ProposalKey, summary.SummaryID, summary.ExpectedRevision = "revise", "", job.CurrentSummary.SummaryID, &revision
	outcomes := summarizedNodeOutcome()
	outcomes[0].SummaryKeys = []string{summary.SummaryID}
	_, err := BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "does not match")
	job.RemovedMessageIDs = []string{"old-message"}
	_, err = BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "unavailable source")
}

func TestNodePublicationGivenNoSummaryOutcomeThenCannotPublishThatMessageElsewhere(t *testing.T) {
	_, err := BuildNodePublication(
		nodeTestJob(),
		nodeBatchOutput(
			t,
			[]generatedNodeSummary{newNodeProposal()},
			[]generatedNodeOutcome{
				{MessageID: "message-1", Action: "no_summary", Reason: "No conclusion"},
			},
		),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "cannot also be used")
}

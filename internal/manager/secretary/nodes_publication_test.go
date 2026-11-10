package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func nodeTestJob() domain.SecretaryJob {
	job := rollingTestJob()
	job.BatchID = "batch-one"
	job.CurrentSummaries = []domain.SummaryVersion{*job.CurrentSummary}
	return job
}

func nodeBatchOutput(
	t *testing.T,
	summaries []generatedNodeSummary,
	outcomes []generatedNodeOutcome,
) string {
	t.Helper()
	if summaries == nil {
		summaries = []generatedNodeSummary{}
	}
	if outcomes == nil {
		outcomes = []generatedNodeOutcome{}
	}
	encoded, err := json.Marshal(generatedNodeBatch{Summaries: summaries, Outcomes: outcomes})
	require.NoError(t, err)
	return string(encoded)
}

func newNodeProposal() generatedNodeSummary {
	return generatedNodeSummary{
		Action:           "create",
		ProposalKey:      "database",
		Title:            "Database",
		Body:             "Use the database.",
		SourceMessageIDs: []string{"message-1"},
	}
}

func summarizedNodeOutcome() []generatedNodeOutcome {
	return []generatedNodeOutcome{
		{MessageID: "message-1", Action: "summarized", SummaryKeys: []string{"database"}},
	}
}

func TestNodePublicationGivenSeparateIssuesThenCreatesAndRevisesWithoutHistoricalCoverageUnion(
	t *testing.T,
) {
	job := nodeTestJob()
	revision := job.CurrentSummaries[0].Revision
	create := newNodeProposal()
	revise := generatedNodeSummary{
		Action:           "revise",
		SummaryID:        job.CurrentSummaries[0].SummaryID,
		ExpectedRevision: &revision,
		Title:            "Revised",
		Body:             "Use a database now",
		SourceMessageIDs: []string{"message-1"},
	}
	outcomes := summarizedNodeOutcome()
	outcomes[0].SummaryKeys = append(outcomes[0].SummaryKeys, revise.SummaryID)
	publication, err := BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{create, revise}, outcomes),
		domain.SecretaryToolReads{},
	)
	require.NoError(t, err)
	require.Len(t, publication.Outputs, 2)
	assert.Empty(
		t,
		publication.Outputs[0].Request.SummaryID,
		"the store allocates the stable node ID",
	)
	assert.Equal(t, "database", publication.Outputs[0].ProposalKey)
	assert.Equal(t, job.BatchID, publication.RequestID)
	for _, output := range publication.Outputs {
		require.Len(t, output.Request.Coverage, 1)
		assert.Equal(t, "message-1", output.Request.Coverage[0].MessageID)
	}
	assert.Equal(t, revision, publication.Outputs[1].Request.ExpectedRevision)
}

func TestNodePublicationGivenSelectedUnchangedBaselineThenRetainsOnlyItsExactEvidence(
	t *testing.T,
) {
	job := nodeTestJob()
	revision := job.CurrentSummary.Revision
	summary := generatedNodeSummary{
		Action:           "revise",
		SummaryID:        job.CurrentSummary.SummaryID,
		ExpectedRevision: &revision,
		Body:             "Keep earlier decision",
		SourceMessageIDs: []string{"old-message"},
		Claims: []generatedClaim{
			{
				Text: "Earlier decision",
				Evidence: []generatedEvidence{
					{MessageID: "old-message", Quote: "Earlier decision"},
				},
			},
		},
	}
	publication, err := BuildNodePublication(
		job,
		nodeBatchOutput(
			t,
			[]generatedNodeSummary{summary},
			[]generatedNodeOutcome{
				{MessageID: "message-1", Action: "no_summary", Reason: "No new conclusion"},
			},
		),
		domain.SecretaryToolReads{},
	)
	require.NoError(t, err)
	assert.Equal(t, job.CurrentSummary.Coverage, publication.Outputs[0].Request.Coverage)
	assert.Equal(
		t,
		job.CurrentSummary.Claims[0].Evidence,
		publication.Outputs[0].Request.Claims[0].Evidence,
	)
	summary.SourceMessageIDs = []string{"message-1"}
	_, err = BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome()),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "selected summary coverage")
}

func TestNodePublicationGivenNoNewConclusionThenAdvancesOnlyExplicitOutcomes(t *testing.T) {
	publication, err := BuildNodePublication(
		nodeTestJob(),
		nodeBatchOutput(
			t,
			nil,
			[]generatedNodeOutcome{
				{MessageID: "message-1", Action: "no_summary", Reason: "Repeated statement"},
			},
		),
		domain.SecretaryToolReads{},
	)
	require.NoError(t, err)
	assert.Empty(t, publication.Outputs)
	assert.Len(t, publication.Outcomes, 1)
}

func TestNodePublicationGivenInvalidProcessingOutcomeThenRejectsTheWholeBatch(t *testing.T) {
	for name, outcomes := range map[string][]generatedNodeOutcome{
		"missing":          {},
		"foreign":          {{MessageID: "foreign", Action: "no_summary", Reason: "x"}},
		"duplicate":        {{MessageID: "message-1", Action: "no_summary", Reason: "x"}, {MessageID: "message-1", Action: "no_summary", Reason: "x"}},
		"unknown action":   {{MessageID: "message-1", Action: "ignored"}},
		"no reason":        {{MessageID: "message-1", Action: "no_summary"}},
		"mixed no summary": {{MessageID: "message-1", Action: "no_summary", Reason: "x", SummaryKeys: []string{"database"}}},
		"empty keys":       {{MessageID: "message-1", Action: "summarized"}},
		"unknown key":      {{MessageID: "message-1", Action: "summarized", SummaryKeys: []string{"missing"}}},
		"duplicate keys":   {{MessageID: "message-1", Action: "summarized", SummaryKeys: []string{"database", "database"}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildNodePublication(
				nodeTestJob(),
				nodeBatchOutput(t, []generatedNodeSummary{newNodeProposal()}, outcomes),
				domain.SecretaryToolReads{},
			)
			assert.Error(t, err)
		})
	}
}

func TestNodePublicationGivenUnavailableBaselineOrSourceThenRejects(t *testing.T) {
	for name, mutate := range map[string]func(*domain.SecretaryJob, *generatedNodeSummary){
		"foreign baseline": func(job *domain.SecretaryJob, _ *generatedNodeSummary) { job.CurrentSummaries[0].OwnerUserID = "other" },
		"invalid baseline": func(job *domain.SecretaryJob, _ *generatedNodeSummary) { job.CurrentSummaries[0].Revision = 0 },
		"source missing": func(_ *domain.SecretaryJob, summary *generatedNodeSummary) {
			summary.SourceMessageIDs = []string{"unknown"}
		},
		"no source": func(_ *domain.SecretaryJob, summary *generatedNodeSummary) { summary.SourceMessageIDs = nil },
		"not read": func(_ *domain.SecretaryJob, summary *generatedNodeSummary) {
			rev := int64(7)
			summary.Action, summary.ProposalKey, summary.SummaryID, summary.ExpectedRevision = "revise", "", "unknown", &rev
		},
		"wrong revision": func(job *domain.SecretaryJob, summary *generatedNodeSummary) {
			rev := int64(7)
			summary.Action, summary.ProposalKey, summary.SummaryID, summary.ExpectedRevision = "revise", "", job.CurrentSummary.SummaryID, &rev
		},
		"no delta":          func(job *domain.SecretaryJob, _ *generatedNodeSummary) { job.Messages = nil },
		"invalid JSON data": func(_ *domain.SecretaryJob, summary *generatedNodeSummary) { summary.Body = "" },
	} {
		t.Run(name, func(t *testing.T) {
			job, summary := nodeTestJob(), newNodeProposal()
			mutate(&job, &summary)
			_, err := BuildNodePublication(
				job,
				nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome()),
				domain.SecretaryToolReads{},
			)
			assert.Error(t, err)
		})
	}
}

func TestNodePublicationGivenVerifiedAdditionalReadThenBindsProofWithoutMutatingIt(t *testing.T) {
	job := nodeTestJob()
	ref := domain.MessageVersionRef{
		MessageID:               "other",
		ContentHash:             domain.SearchContentHash("Related decision"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		ReadReceipt:             "proof",
	}
	summary := newNodeProposal()
	summary.RelatedSources = []domain.MessageVersionRef{ref}
	summary.Claims = []generatedClaim{
		{
			Text: "Related decision",
			Evidence: []generatedEvidence{
				{MessageID: "other", Quote: "Related decision", View: "full", ReadReceipt: "proof"},
			},
		},
	}
	reads := domain.SecretaryToolReads{
		Messages: []domain.SecretaryReadMessage{
			{Ref: ref, Text: "Related decision", View: "full", ReadReceipt: "proof"},
		},
	}
	publication, err := BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome()),
		reads,
	)
	require.NoError(t, err)
	require.Len(t, publication.Outputs[0].Request.Coverage, 2)
	assert.Equal(t, "proof", publication.Outputs[0].Request.Coverage[1].ReadReceipt)
	assert.Equal(t, "proof", publication.Outputs[0].Request.Claims[0].Evidence[0].ReadReceipt)
	_, err = BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome()),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "verified read")
}

func TestNodePublicationGivenSummaryReadOutsideFirstPageThenRequiresConsistentExactRevision(
	t *testing.T,
) {
	job := nodeTestJob()
	baseline := job.CurrentSummaries[0]
	baseline.SummaryID = "another-node"
	revision := baseline.Revision
	summary := generatedNodeSummary{
		Action:           "revise",
		SummaryID:        baseline.SummaryID,
		ExpectedRevision: &revision,
		Body:             "Updated",
		SourceMessageIDs: []string{"message-1"},
	}
	reads := domain.SecretaryToolReads{SummaryVersions: []domain.SummaryVersion{baseline}}
	outcomes := summarizedNodeOutcome()
	outcomes[0].SummaryKeys = []string{baseline.SummaryID}
	_, err := BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes),
		reads,
	)
	require.NoError(t, err)
	baseline.SummaryID, baseline.Revision = job.CurrentSummaries[0].SummaryID, 10
	reads.SummaryVersions = []domain.SummaryVersion{baseline}
	_, err = BuildNodePublication(
		job,
		nodeBatchOutput(t, []generatedNodeSummary{summary}, outcomes),
		reads,
	)
	assert.ErrorIs(t, err, domain.ErrSummaryVersionMismatch)
}

func TestNodePublicationGivenManyOutputsThenEnforcesOneBoundBudget(t *testing.T) {
	summary := newNodeProposal()
	summary.Body = strings.Repeat("x", 53*1024)
	var summaries []generatedNodeSummary
	for _, key := range []string{"database", "second", "third", "fourth", "fifth"} {
		item := summary
		item.ProposalKey = key
		summaries = append(summaries, item)
	}
	_, err := BuildNodePublication(
		nodeTestJob(),
		nodeBatchOutput(t, summaries, summarizedNodeOutcome()),
		domain.SecretaryToolReads{},
	)
	assert.ErrorContains(t, err, "64 KiB")
}

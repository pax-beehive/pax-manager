package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func sourceOnlyJob() domain.SecretaryJob {
	job := nodeTestJob()
	job.OutputPolicyVersion = domain.SecretarySourceOnlyOutputPolicyVersion
	return job
}

func sourceOnlyClaims(t *testing.T, output, claims string, nodes bool) string {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &value))
	if nodes {
		value["summaries"].([]any)[0].(map[string]any)["claims"] = json.RawMessage(claims)
	} else {
		value["claims"] = json.RawMessage(claims)
	}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestSourceOnlyPublicationGivenIgnoredClaimsThenPublishesOnlyTextAndExactCoverage(
	t *testing.T,
) {
	for name, claims := range map[string]string{
		"unsupported quote":    `[{"text":"A fact","evidence":[{"message_id":"message-1","quote":"This was never said","read_receipt":"untrusted"}]}]`,
		"legacy metadata":      `[{"id":"obsolete","text":"A fact","evidence":[{"message":{"message_id":"foreign"},"quote":"wrong"}],"unused":true}]`,
		"wrong shape":          `{"ignored":"metadata"}`,
		"over old claim limit": `[` + strings.Repeat(`{"text":"ignored"},`, 100) + `{"text":"ignored"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			job := sourceOnlyJob()
			output := sourceOnlyClaims(t, nodeBatchOutput(t,
				[]generatedNodeSummary{newNodeProposal()}, summarizedNodeOutcome()), claims, true)
			publication, err := BuildNodePublication(job, output, domain.SecretaryToolReads{})
			require.NoError(t, err)
			require.Len(t, publication.Outputs, 1)
			request := publication.Outputs[0].Request
			assert.Equal(t, "Database", request.Title)
			assert.Equal(t, "Use the database.", request.Body)
			assert.Empty(t, request.Claims)
			assert.Equal(
				t,
				[]domain.MessageVersionRef{messageReference(job.Messages[0])},
				request.Coverage,
			)
			assert.Len(t, publication.Outcomes, 1)
			refs, err := RequestedNodeSourceReads(job, output)
			require.NoError(t, err)
			assert.Empty(t, refs)

			legacyOutput := sourceOnlyClaims(
				t,
				`{"title":"Database","body":"Use the database."}`,
				claims,
				false,
			)
			legacyPublication, err := BuildPublication(job, legacyOutput)
			require.NoError(t, err)
			assert.Empty(t, legacyPublication.Claims)
			assert.Equal(
				t,
				[]domain.MessageVersionRef{messageReference(job.Messages[0])},
				legacyPublication.Coverage,
			)
			refs, err = RequestedSourceReads(job, legacyOutput)
			require.NoError(t, err)
			assert.Empty(t, refs)
		})
	}
}

func TestSourceOnlyRevisionGivenLegacyBaselineThenDropsClaimsWithoutChangingOldVersion(
	t *testing.T,
) {
	job := sourceOnlyJob()
	before, err := json.Marshal(job.CurrentSummaries[0])
	require.NoError(t, err)
	baseline := job.CurrentSummaries[0]
	proposal := generatedNodeSummary{Action: "revise", SummaryID: baseline.SummaryID,
		ExpectedRevision: &baseline.Revision, Title: "Updated decision", Body: "The current decision.",
		SourceMessageIDs: []string{"message-1"},
		Claims: []generatedClaim{
			{
				Text:     "Unverified copy",
				Evidence: []generatedEvidence{{MessageID: "old-message", Quote: "incorrect"}},
			},
		},
	}
	outcomes := summarizedNodeOutcome()
	outcomes[0].SummaryKeys = []string{baseline.SummaryID}
	publication, err := BuildNodePublication(job,
		nodeBatchOutput(t, []generatedNodeSummary{proposal}, outcomes), domain.SecretaryToolReads{})
	require.NoError(t, err)
	request := publication.Outputs[0].Request
	assert.Empty(t, request.Claims)
	assert.Equal(t, baseline.Revision, request.ExpectedRevision)
	assert.Equal(t, []domain.MessageVersionRef{messageReference(job.Messages[0])}, request.Coverage)
	after, err := json.Marshal(job.CurrentSummaries[0])
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func TestSourceOnlyReadsGivenExplicitRelatedSourcesThenStillRequiresVerifiedVersion(t *testing.T) {
	job := sourceOnlyJob()
	ref := domain.MessageVersionRef{
		MessageID:               "other",
		ContentHash:             domain.SearchContentHash("Related"),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion,
		ReadReceipt:             "proof",
	}
	proposal := newNodeProposal()
	proposal.RelatedSources = []domain.MessageVersionRef{ref}
	proposal.Claims = []generatedClaim{{Text: "Ignored", Evidence: []generatedEvidence{{
		MessageID: "message-1", Message: &ref, Quote: "wrong", ReadReceipt: "conflicting",
	}}}}
	output := nodeBatchOutput(t, []generatedNodeSummary{proposal}, summarizedNodeOutcome())
	refs, err := RequestedNodeSourceReads(job, output)
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, refs)
	reads := domain.SecretaryToolReads{Messages: []domain.SecretaryReadMessage{{
		Ref: ref, View: "full", Text: "Related", ReadReceipt: ref.ReadReceipt,
	}}}
	publication, err := BuildNodePublication(job, output, reads)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]domain.MessageVersionRef{messageReference(job.Messages[0]), ref},
		publication.Outputs[0].Request.Coverage,
	)
	assert.Empty(t, publication.Outputs[0].Request.Claims)
	_, err = BuildNodePublication(job, output, domain.SecretaryToolReads{})
	assert.ErrorContains(t, err, "verified read")

	legacyOutput, err := json.Marshal(generatedSummary{Title: "Related", Body: "Supported text",
		RelatedSources: proposal.RelatedSources, Claims: proposal.Claims})
	require.NoError(t, err)
	refs, err = RequestedSourceReads(job, string(legacyOutput))
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, refs)
	refs, err = RequestedRelatedSourcesForPolicy(string(legacyOutput), job.OutputPolicyVersion)
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, refs)
	request, err := BuildPublicationWithReads(job, string(legacyOutput), reads)
	require.NoError(t, err)
	assert.Len(t, request.Coverage, 2)
	assert.Empty(t, request.Claims)
	_, err = BuildPublication(job, string(legacyOutput))
	assert.ErrorContains(t, err, "verified read")
	reads.Messages[0].Ref.ContentHash = domain.SearchContentHash("Changed")
	_, err = BuildNodePublication(job, output, reads)
	assert.ErrorContains(t, err, "verified read")
}

func TestSourceOnlyPublicationGivenInvalidSourcesOutcomesOrSchemaThenStillRejects(t *testing.T) {
	for name, mutate := range map[string]func(*generatedNodeSummary, *[]generatedNodeOutcome){
		"unavailable source": func(s *generatedNodeSummary, _ *[]generatedNodeOutcome) { s.SourceMessageIDs = []string{"foreign"} },
		"no source":          func(s *generatedNodeSummary, _ *[]generatedNodeOutcome) { s.SourceMessageIDs = nil },
		"duplicate source": func(s *generatedNodeSummary, _ *[]generatedNodeOutcome) {
			s.SourceMessageIDs = []string{"message-1", "message-1"}
		},
		"missing outcome": func(_ *generatedNodeSummary, o *[]generatedNodeOutcome) { *o = []generatedNodeOutcome{} },
		"wrong outcome":   func(_ *generatedNodeSummary, o *[]generatedNodeOutcome) { (*o)[0].SummaryKeys = []string{"wrong"} },
		"empty body":      func(s *generatedNodeSummary, _ *[]generatedNodeOutcome) { s.Body = "" },
		"body budget": func(s *generatedNodeSummary, _ *[]generatedNodeOutcome) {
			s.Body = strings.Repeat("x", domain.SecretaryPublicationBatchMaxBytes)
		},
	} {
		t.Run(name, func(t *testing.T) {
			proposal, outcomes := newNodeProposal(), summarizedNodeOutcome()
			mutate(&proposal, &outcomes)
			_, err := BuildNodePublication(sourceOnlyJob(), nodeBatchOutput(t,
				[]generatedNodeSummary{proposal}, outcomes), domain.SecretaryToolReads{})
			assert.Error(t, err)
		})
	}
	for _, output := range []string{
		`{"summaries":[],"outcomes":[],"unexpected":true}`,
		`{"summaries":null,"outcomes":[]}`,
		`{"summaries":[],"outcomes":null}`,
		`{"summaries":[{"action":"create","proposal_key":"x","body":"x","unexpected":true}],"outcomes":[]}`,
		`{"summaries":[],"outcomes":[]} {}`,
		strings.Repeat("x", 1024*1024+1),
	} {
		_, err := BuildNodePublication(sourceOnlyJob(), output, domain.SecretaryToolReads{})
		assert.Error(t, err)
	}
}

func TestSourceOnlyPublicationGivenRoutineInputThenAcceptsOnlyProcessingOutcome(t *testing.T) {
	output := nodeBatchOutput(t, nil, []generatedNodeOutcome{{
		MessageID: "message-1", Action: "no_summary", Reason: "Repeated activity",
	}})
	publication, err := BuildNodePublication(sourceOnlyJob(), output, domain.SecretaryToolReads{})
	require.NoError(t, err)
	assert.Empty(t, publication.Outputs)
	assert.Len(t, publication.Outcomes, 1)
	refs, err := RequestedNodeSourceReads(sourceOnlyJob(), output)
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestOutputPolicyGivenUnsupportedOrLegacyThenKeepsExplicitContracts(t *testing.T) {
	job := sourceOnlyJob()
	nodeOutput := sourceOnlyClaims(
		t,
		nodeBatchOutput(t,
			[]generatedNodeSummary{newNodeProposal()}, summarizedNodeOutcome()),
		`[{"text":"Unsupported","evidence":[{"message_id":"message-1","quote":"never exposed"}]}]`,
		true,
	)
	legacyOutput := `{"body":"Updated","claims":[{"text":"Unsupported","evidence":[{"message_id":"message-1","quote":"never exposed"}]}]}`
	for _, policy := range []string{"", domain.SecretaryLegacyOutputPolicyVersion} {
		job.OutputPolicyVersion = policy
		_, err := BuildNodePublication(job, nodeOutput, domain.SecretaryToolReads{})
		assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))
		_, err = BuildPublication(job, legacyOutput)
		assert.Equal(t, "evidence", domain.SecretaryValidationFailureCode(err))
	}
	job.OutputPolicyVersion = "unsupported-future-policy"
	_, err := BuildNodePublication(job, nodeOutput, domain.SecretaryToolReads{})
	assert.Error(t, err)
	_, err = RequestedNodeSourceReads(job, nodeOutput)
	assert.Error(t, err)
	_, err = BuildPublication(job, legacyOutput)
	assert.Error(t, err)
	_, err = RequestedSourceReads(job, legacyOutput)
	assert.Error(t, err)
	_, err = RequestedRelatedSourcesForPolicy(legacyOutput, job.OutputPolicyVersion)
	assert.Error(t, err)
}

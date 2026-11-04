package secretary

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeOutputGivenIndependentIssuesThenAcceptsMultipleSummaryProposals(t *testing.T) {
	output := `{"summaries":[{"action":"create","proposal_key":"search","title":"Search","body":"Use ES","source_message_ids":["m1"]},{"action":"revise","summary_id":"existing","expected_revision":2,"title":"Deploy","body":"Deploy later","source_message_ids":["m2"]}],"outcomes":[{"message_id":"m1","action":"summarized","summary_keys":["search"]},{"message_id":"m2","action":"summarized","summary_keys":["existing"]}]}`
	batch, err := decodeGeneratedNodeBatch(output)
	require.NoError(t, err)
	require.Len(t, batch.Summaries, 2)
	assert.Equal(t, "search", batch.Summaries[0].ProposalKey)
	assert.EqualValues(t, 2, *batch.Summaries[1].ExpectedRevision)
	assert.Len(t, batch.Outcomes, 2)
	batch, err = decodeGeneratedNodeBatch(
		`{"summaries":[],"outcomes":[{"message_id":"m1","action":"no_summary","reason":"No new conclusions"}]}`,
	)
	require.NoError(t, err)
	assert.Empty(t, batch.Summaries)
}

func TestNodeOutputGivenManyIndependentReadsThenCapsRecoveryVerificationBeforeDatabaseReads(
	t *testing.T,
) {
	batch := generatedNodeBatch{Outcomes: []generatedNodeOutcome{}}
	for index := range 11 {
		summary := generatedNodeSummary{
			Action:      "create",
			ProposalKey: strconv.Itoa(index),
			Body:        "Summary",
		}
		for source := range 50 {
			summary.RelatedSources = append(
				summary.RelatedSources,
				domain.MessageVersionRef{
					MessageID:               strconv.Itoa(index*50 + source),
					ContentHash:             "hash",
					CanonicalizationVersion: "v1",
					ReadReceipt:             "proof",
				},
			)
		}
		batch.Summaries = append(batch.Summaries, summary)
	}
	encoded, err := json.Marshal(batch)
	require.NoError(t, err)
	_, err = RequestedNodeSourceReads(testJob(), string(encoded))
	assert.ErrorContains(t, err, "source read limit")
}

func TestNodeOutputGivenInvalidSchemaThenRejectsWithoutPartialAcceptance(t *testing.T) {
	for name, output := range map[string]string{
		"malformed":          "not json",
		"oversized":          strings.Repeat("x", 1024*1024+1),
		"trailing":           `{"summaries":[],"outcomes":[]} {}`,
		"missing arrays":     `{}`,
		"unknown field":      `{"summaries":[],"outcomes":[],"owner":"other"}`,
		"topic":              `{"summaries":[{"action":"topic","body":"x"}],"outcomes":[]}`,
		"missing proposal":   `{"summaries":[{"action":"create","body":"x"}],"outcomes":[]}`,
		"assigned create id": `{"summaries":[{"action":"create","proposal_key":"a","summary_id":"chosen","body":"x"}],"outcomes":[]}`,
		"create revision":    `{"summaries":[{"action":"create","proposal_key":"a","expected_revision":0,"body":"x"}],"outcomes":[]}`,
		"missing revision":   `{"summaries":[{"action":"revise","summary_id":"a","body":"x"}],"outcomes":[]}`,
		"zero revision":      `{"summaries":[{"action":"revise","summary_id":"a","expected_revision":0,"body":"x"}],"outcomes":[]}`,
		"revision proposal":  `{"summaries":[{"action":"revise","summary_id":"a","proposal_key":"b","expected_revision":1,"body":"x"}],"outcomes":[]}`,
		"empty body":         `{"summaries":[{"action":"create","proposal_key":"a","body":" "}],"outcomes":[]}`,
		"duplicate sources":  `{"summaries":[{"action":"create","proposal_key":"a","body":"x","source_message_ids":["m","m"]}],"outcomes":[]}`,
		"empty source":       `{"summaries":[{"action":"create","proposal_key":"a","body":"x","source_message_ids":[""]}],"outcomes":[]}`,
		"duplicate key":      `{"summaries":[{"action":"create","proposal_key":"a","body":"x"},{"action":"create","proposal_key":"a","body":"y"}],"outcomes":[]}`,
		"untrusted ref":      `{"summaries":[{"action":"create","proposal_key":"a","body":"x","related_sources":[{"message_id":"m"}]}],"outcomes":[]}`,
	} {
		t.Run(
			name,
			func(t *testing.T) { _, err := decodeGeneratedNodeBatch(output); assert.Error(t, err) },
		)
	}
}

func TestNodeOutputGivenManySummariesThenEnforcesAggregateClaimAndEvidenceLimits(t *testing.T) {
	for _, evidenceCount := range []int{0, 513} {
		batch := generatedNodeBatch{
			Summaries: []generatedNodeSummary{{Action: "create", ProposalKey: "a", Body: "x"}},
			Outcomes:  []generatedNodeOutcome{},
		}
		if evidenceCount == 0 {
			batch.Summaries[0].Claims = make([]generatedClaim, 101)
		} else {
			batch.Summaries[0].Claims = []generatedClaim{{Text: "x", Evidence: make([]generatedEvidence, evidenceCount)}}
		}
		encoded, err := json.Marshal(batch)
		require.NoError(t, err)
		_, err = decodeGeneratedNodeBatch(string(encoded))
		assert.Error(t, err)
	}
}

func TestNodeOutputGivenRepeatedVerifiedReadThenDeduplicatesAcrossSummaries(t *testing.T) {
	job := testJob()
	ref := messageReference(job.Messages[0])
	ref.ReadReceipt = "receipt"
	summary := generatedNodeSummary{
		Action:         "create",
		ProposalKey:    "a",
		Body:           "x",
		RelatedSources: []domain.MessageVersionRef{ref},
	}
	batch := generatedNodeBatch{
		Summaries: []generatedNodeSummary{summary},
		Outcomes:  []generatedNodeOutcome{},
	}
	summary.ProposalKey = "b"
	batch.Summaries = append(batch.Summaries, summary)
	encoded, err := json.Marshal(batch)
	require.NoError(t, err)
	refs, err := RequestedNodeSourceReads(job, string(encoded))
	require.NoError(t, err)
	assert.Equal(t, []domain.MessageVersionRef{ref}, refs)
	_, err = RequestedNodeSourceReads(job, "invalid")
	assert.Error(t, err)
}

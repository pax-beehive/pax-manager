package secretary

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRequestedSourceReadsGivenExpansionEvidenceThenSelectsExactDeltaVersions(t *testing.T) {
	for name, evidence := range map[string]generatedEvidence{
		"simple proof": {MessageID: "message-1", Quote: "Earlier", ReadReceipt: "proof"},
		"rich proof": {Quote: "Earlier", Message: &domain.MessageVersionRef{
			MessageID: "message-1", ContentHash: paragraphJob().Messages[0].ContentHash,
			CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "proof",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			job := paragraphJob()
			output, err := json.Marshal(
				generatedSummary{
					Body: "Updated",
					Claims: []generatedClaim{
						{Text: "Earlier", Evidence: []generatedEvidence{evidence, evidence}},
					},
				},
			)
			require.NoError(t, err)
			refs, err := RequestedSourceReads(job, string(output))
			require.NoError(t, err)
			ref := messageReference(job.Messages[0])
			ref.ReadReceipt = "proof"
			assert.Equal(t, []domain.MessageVersionRef{ref}, refs)
		})
	}
}

func TestRequestedSourceReadsGivenConflictingProofThenRejects(t *testing.T) {
	for name, evidence := range map[string]generatedEvidence{
		"wrong message alias": {MessageID: "wrong", Message: &domain.MessageVersionRef{MessageID: "message-1", ReadReceipt: "proof"}},
		"wrong version":       {MessageID: "message-1", ReadReceipt: "proof", Message: &domain.MessageVersionRef{MessageID: "message-1", ContentHash: "wrong", CanonicalizationVersion: domain.SearchCanonicalizationVersion}},
		"conflicting receipt": {MessageID: "message-1", ReadReceipt: "proof", Message: &domain.MessageVersionRef{MessageID: "message-1", ReadReceipt: "different"}},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := json.Marshal(
				generatedSummary{
					Body: "Updated",
					Claims: []generatedClaim{
						{Text: "Earlier", Evidence: []generatedEvidence{evidence}},
					},
				},
			)
			require.NoError(t, err)
			_, err = RequestedSourceReads(paragraphJob(), string(output))
			assert.Error(t, err)
		})
	}
	_, err := RequestedSourceReads(paragraphJob(), "invalid JSON")
	assert.Error(t, err)
	output, err := json.Marshal(
		generatedSummary{
			Body:   "Updated",
			Claims: []generatedClaim{{Text: "Too many", Evidence: make([]generatedEvidence, 513)}},
		},
	)
	require.NoError(t, err)
	_, err = RequestedSourceReads(paragraphJob(), string(output))
	assert.Error(t, err)
}

func TestRequestedSourceReadsGivenUnselectedForeignEvidenceThenDoesNotAuthorizeIt(t *testing.T) {
	job := paragraphJob()
	output := `{"body":"Updated","claims":[{"text":"Foreign","evidence":[{"message_id":"foreign","quote":"secret","read_receipt":"proof"}]}]}`
	refs, err := RequestedSourceReads(job, output)
	require.NoError(t, err)
	assert.Empty(t, refs)
	_, err = BuildPublicationWithReads(
		job,
		output,
		domain.SecretaryToolReads{Messages: []domain.SecretaryReadMessage{{
			Ref: domain.MessageVersionRef{
				MessageID:               "foreign",
				ContentHash:             "hash",
				CanonicalizationVersion: domain.SearchCanonicalizationVersion,
				ReadReceipt:             "proof",
			},
			Text: "secret", View: "full", ReadReceipt: "proof",
		}}},
	)
	assert.Error(t, err)
}

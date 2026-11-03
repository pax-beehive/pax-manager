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

func TestRequestedSourceReadsGivenSelectedVersionAndIndependentEvidenceReceiptThenRequestsBoth(
	t *testing.T,
) {
	selected := domain.MessageVersionRef{
		MessageID: "related", ContentHash: domain.SearchContentHash("First page. Second page."),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "first-page",
	}
	second := selected
	second.ReadReceipt = "second-page"
	for name, evidence := range map[string]generatedEvidence{
		"simple": {MessageID: selected.MessageID, Quote: "Second page.", ReadReceipt: second.ReadReceipt},
		"rich":   {Message: &second, Quote: "Second page."},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := json.Marshal(generatedSummary{
				Body: "Updated", RelatedSources: []domain.MessageVersionRef{selected},
				Claims: []generatedClaim{
					{Text: "Second page", Evidence: []generatedEvidence{evidence, evidence}},
				},
			})
			require.NoError(t, err)
			refs, err := RequestedSourceReads(paragraphJob(), string(output))
			require.NoError(t, err)
			assert.Equal(t, []domain.MessageVersionRef{selected, second}, refs)
		})
	}
}

func TestRequestedSourceReadsGivenIndependentEvidenceForDifferentVersionThenRejects(t *testing.T) {
	selected := domain.MessageVersionRef{
		MessageID: "related", ContentHash: "selected-hash",
		CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "first-page",
	}
	for name, mutate := range map[string]func(*domain.MessageVersionRef){
		"hash":             func(ref *domain.MessageVersionRef) { ref.ContentHash = "different" },
		"canonicalization": func(ref *domain.MessageVersionRef) { ref.CanonicalizationVersion = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			other := selected
			other.ReadReceipt = "second-page"
			mutate(&other)
			output, err := json.Marshal(generatedSummary{
				Body: "Updated", RelatedSources: []domain.MessageVersionRef{selected},
				Claims: []generatedClaim{{Text: "Second page", Evidence: []generatedEvidence{{
					Message: &other, Quote: "Second page.",
				}}}},
			})
			require.NoError(t, err)
			_, err = RequestedSourceReads(paragraphJob(), string(output))
			assert.Error(t, err)
		})
	}
}

func TestRequestedSourceReadsGivenMultipleSelectedVersionsThenRequiresExactEvidenceVersion(
	t *testing.T,
) {
	first := domain.MessageVersionRef{
		MessageID: "related", ContentHash: "first-version",
		CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "first-page",
	}
	second := first
	second.ContentHash, second.ReadReceipt = "second-version", "second-page"
	for name, evidence := range map[string]generatedEvidence{
		"ambiguous":        {MessageID: first.MessageID, Quote: "Text", ReadReceipt: "independent-page"},
		"selected receipt": {MessageID: first.MessageID, Quote: "Text", ReadReceipt: second.ReadReceipt},
		"exact version": {Message: &domain.MessageVersionRef{
			MessageID: first.MessageID, ContentHash: first.ContentHash,
			CanonicalizationVersion: first.CanonicalizationVersion, ReadReceipt: "independent-page",
		}, Quote: "Text"},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := json.Marshal(generatedSummary{
				Body: "Updated", RelatedSources: []domain.MessageVersionRef{first, second},
				Claims: []generatedClaim{{Text: "Text", Evidence: []generatedEvidence{evidence}}},
			})
			require.NoError(t, err)
			refs, err := RequestedSourceReads(paragraphJob(), string(output))
			if name == "ambiguous" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if name == "selected receipt" {
				assert.Equal(t, []domain.MessageVersionRef{first, second}, refs)
				return
			}
			expected := first
			expected.ReadReceipt = "independent-page"
			assert.Equal(t, expected, refs[2])
		})
	}
}

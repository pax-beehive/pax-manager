package secretary

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRollingPublicationGivenSelectedVersionAndIndependentReadThenBindsExactEvidence(
	t *testing.T,
) {
	selected := domain.MessageVersionRef{
		MessageID: "related", ContentHash: domain.SearchContentHash("First page. Second page."),
		CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "selected-page",
	}
	second := selected
	second.ReadReceipt = "evidence-page"
	for _, view := range []string{"full", "first", "last"} {
		t.Run(view, func(t *testing.T) {
			output, err := json.Marshal(generatedSummary{
				Body: "Updated", RelatedSources: []domain.MessageVersionRef{selected},
				Claims: []generatedClaim{{Text: "Second page", Evidence: []generatedEvidence{{
					MessageID: second.MessageID, Quote: "Second page.", View: view, ReadReceipt: second.ReadReceipt,
				}}}},
			})
			require.NoError(t, err)
			reads := domain.SecretaryToolReads{Messages: []domain.SecretaryReadMessage{
				{
					Ref:         selected,
					Text:        "First page.",
					View:        "last",
					ReadReceipt: selected.ReadReceipt,
				},
				{Ref: second, Text: "Second page.", View: view, ReadReceipt: second.ReadReceipt},
			}}
			request, err := BuildPublicationWithReads(testJob(), string(output), reads)
			require.NoError(t, err)
			assert.Equal(t, domain.SummaryEvidence{
				Message: second, Quote: "Second page.", View: view, ReadReceipt: second.ReadReceipt,
			}, request.Claims[0].Evidence[0])
			assert.Equal(
				t,
				selected,
				request.Coverage[1],
				"coverage still contains one selected version",
			)
		})
	}
}

func TestRollingPublicationGivenIndependentEvidenceReadMismatchThenRejects(t *testing.T) {
	selected := domain.MessageVersionRef{
		MessageID: "related", ContentHash: "selected-hash",
		CanonicalizationVersion: domain.SearchCanonicalizationVersion, ReadReceipt: "selected-page",
	}
	second := selected
	second.ReadReceipt = "evidence-page"
	output, err := json.Marshal(generatedSummary{
		Body: "Updated", RelatedSources: []domain.MessageVersionRef{selected},
		Claims: []generatedClaim{{Text: "Second page", Evidence: []generatedEvidence{{
			MessageID: second.MessageID, Quote: "Second page.", View: "full", ReadReceipt: second.ReadReceipt,
		}}}},
	})
	require.NoError(t, err)
	for name, mutate := range map[string]func(*domain.SecretaryReadMessage){
		"hash":             func(read *domain.SecretaryReadMessage) { read.Ref.ContentHash = "different" },
		"canonicalization": func(read *domain.SecretaryReadMessage) { read.Ref.CanonicalizationVersion = "different" },
		"receipt":          func(read *domain.SecretaryReadMessage) { read.ReadReceipt = "different" },
		"view":             func(read *domain.SecretaryReadMessage) { read.View = "last" },
		"quote":            func(read *domain.SecretaryReadMessage) { read.Text = "Unseen text." },
	} {
		t.Run(name, func(t *testing.T) {
			read := domain.SecretaryReadMessage{
				Ref: second, Text: "Second page.", View: "full", ReadReceipt: second.ReadReceipt,
			}
			mutate(&read)
			reads := domain.SecretaryToolReads{Messages: []domain.SecretaryReadMessage{
				{
					Ref:         selected,
					Text:        "First page.",
					View:        "last",
					ReadReceipt: selected.ReadReceipt,
				}, read,
			}}
			_, err := BuildPublicationWithReads(testJob(), string(output), reads)
			assert.Error(t, err)
		})
	}
}

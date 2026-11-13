package secretary

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestEvidenceGivenVerifiedReceiptThenDoesNotRequireDuplicatedNestedProof(t *testing.T) {
	for _, proof := range []string{"top_only", "nested_only", "both"} {
		t.Run(proof, func(t *testing.T) {
			job := paragraphJob()
			ref := messageReference(job.Messages[0])
			readRef := ref
			readRef.ReadReceipt = "verified-page"
			evidence := generatedEvidence{Message: &ref, Quote: "Earlier detailed", View: "full"}
			if proof != "nested_only" {
				evidence.ReadReceipt = "verified-page"
			}
			if proof != "top_only" {
				ref.ReadReceipt = "verified-page"
			}
			reads := []domain.SecretaryReadMessage{
				{
					Ref:         readRef,
					Text:        job.Messages[0].Body,
					View:        "full",
					ReadReceipt: "verified-page",
				},
			}
			bound, err := bindRollingEvidence(evidence, job, nil, reads)
			require.NoError(t, err)
			assert.Equal(t, "verified-page", bound.ReadReceipt)
			assert.Equal(t, readRef, bound.Message)
		})
	}
}

func TestEvidenceGivenNestedMessageThenRejectsConflictingVersionViewOrProof(t *testing.T) {
	for _, scenario := range []string{"hash", "version", "view", "nested_receipt", "top_receipt", "unread"} {
		t.Run(scenario, func(t *testing.T) {
			job := paragraphJob()
			ref := messageReference(job.Messages[0])
			ref.ReadReceipt = "verified-page"
			evidence := generatedEvidence{
				Message:     &ref,
				Quote:       "Earlier detailed",
				View:        "full",
				ReadReceipt: "verified-page",
			}
			reads := []domain.SecretaryReadMessage{
				{Ref: ref, Text: job.Messages[0].Body, View: "full", ReadReceipt: "verified-page"},
			}
			switch scenario {
			case "hash":
				ref.ContentHash = "wrong"
			case "version":
				ref.CanonicalizationVersion = "wrong"
			case "view":
				evidence.View = "last"
			case "nested_receipt":
				ref.ReadReceipt = "different"
			case "top_receipt":
				evidence.ReadReceipt = "unverified"
			case "unread":
				reads = nil
			}
			_, err := bindRollingEvidence(evidence, job, nil, reads)
			assert.Error(t, err)
		})
	}
}

package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretaryOperationTargetGivenReconciliationThenCanonicalReferenceDoesNotUseScanRevisionOrPreview(
	t *testing.T,
) {
	target := SecretaryOperationTarget{
		MessageID: "message-1", ContentHash: SearchContentHash("Complete original"),
		CanonicalizationVersion: SearchCanonicalizationVersion, ChangeRevision: 4,
		Preview: "Complete...",
	}
	reference := target.MessageRef()
	assert.Equal(t, "message-1", reference.MessageID)
	assert.Equal(t, SearchContentHash("Complete original"), reference.ContentHash)
	assert.Equal(t, SearchCanonicalizationVersion, reference.CanonicalizationVersion)
	target.ChangeRevision, target.Preview = 99, "Another bounded preview"
	assert.Equal(t, reference, target.MessageRef())
	target.ContentHash = SearchContentHash("Changed original")
	assert.NotEqual(t, reference.Identity(), target.MessageRef().Identity())
	target.ContentHash, target.MessageID = reference.ContentHash, "message-2"
	assert.NotEqual(t, reference.Identity(), target.MessageRef().Identity())
}

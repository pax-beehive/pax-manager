package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretarySummaryIDGivenSameSourceThenRemainsStableAndOwnerBound(t *testing.T) {
	id := SecretarySummaryID("owner", "source")
	assert.Equal(t, id, SecretarySummaryID("owner", "source"))
	assert.NotEqual(t, id, SecretarySummaryID("other", "source"))
	assert.NotEqual(t, id, SecretarySummaryID("owner", "other"))
	assert.Len(t, id, len("summary_secretary_session_")+32)
}

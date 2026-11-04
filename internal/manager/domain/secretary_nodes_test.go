package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretaryNodeIdentityGivenRetriedProposalThenKeepsIDWithoutMergingDifferentNodes(
	t *testing.T,
) {
	id := SecretaryNodeID("batch", "search")
	assert.Equal(t, id, SecretaryNodeID("batch", "search"))
	assert.NotEqual(t, id, SecretaryNodeID("batch", "deployment"))
	assert.NotEqual(t, id, SecretaryNodeID("later-batch", "search"))
	assert.Contains(t, id, "summary_node_")
}

package secretary

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeBudgetGivenRepeatedVersionsThenFitsWithoutRaisingTheLimit(t *testing.T) {
	first := newNodeProposal()
	first.Body = strings.Repeat("x", 43*1024)
	for i := 0; i < 100; i++ {
		first.Claims = append(
			first.Claims,
			generatedClaim{
				Text:     fmt.Sprintf("Fact %d", i),
				Evidence: []generatedEvidence{{MessageID: "message-1", Quote: "database"}},
			},
		)
	}
	_, err := BuildNodePublication(nodeTestJob(),
		nodeBatchOutput(t, []generatedNodeSummary{first}, summarizedNodeOutcome()),
		domain.SecretaryToolReads{})
	require.NoError(t, err)
}

func TestNodeBudgetGivenOversizedCompactPublicationThenKeepsTheBatchLimit(t *testing.T) {
	summary := newNodeProposal()
	summary.Body = strings.Repeat("x", 64*1024)
	_, err := BuildNodePublication(nodeTestJob(),
		nodeBatchOutput(t, []generatedNodeSummary{summary}, summarizedNodeOutcome()),
		domain.SecretaryToolReads{})
	assert.ErrorIs(t, err, domain.ErrSecretaryBatchPublicationTooLarge)
	assert.Equal(t, "schema", domain.SecretaryValidationFailureCode(err))
}

package storage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryOperationBudgetGivenHumanBodyAtRawLimitThenRejectsOversizedBoundSummary(
	t *testing.T,
) {
	operation, input := operationHelperFixture()
	input.Body = strings.Repeat("x", 64*1024)
	_, err := secretaryOperationSummaryRequest(operation, input)
	assert.ErrorIs(t, err, domain.ErrSecretaryBatchPublicationTooLarge)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestSecretaryOperationGivenInvalidRequestIdentityThenRejectsBeforePersistence(t *testing.T) {
	for _, id := range []string{"   ", "unsafe\x00id", string([]byte{0xff})} {
		_, input := operationHelperFixture()
		input.RequestID = id
		assert.ErrorIs(t, validateSecretaryOperationSubmitFields(input), ErrConflict)
	}
}

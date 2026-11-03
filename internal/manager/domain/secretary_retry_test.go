package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretaryValidationRetryGivenTypedGeneratedOutputThenClassifiesOnlyTrustedFailures(
	t *testing.T,
) {
	for _, code := range []string{"schema", "evidence"} {
		cause := errors.New("generated output rejected")
		failure := SecretaryOutputValidationError{Code: code, Err: cause}
		assert.Equal(t, cause.Error(), failure.Error())
		assert.ErrorIs(t, failure, cause)
		assert.Equal(t, code, SecretaryValidationFailureCode(fmt.Errorf("validation: %w", failure)))
	}
	assert.Equal(t, "evidence", SecretaryValidationFailureCode(ErrSecretaryInvalidReadReceipt))
	for _, failure := range []error{nil, context.Canceled, context.DeadlineExceeded, ErrUnauthorized, ErrConflict, ErrSecretaryFenced,
		errors.New("secretary evidence was not present in the supplied or verified text"),
		errors.New("invalid read receipt; read the exact source version again"),
		errors.New("secretary execution is incomplete; waiting for its existing turn"),
		errors.New("secretary execution requires approval in its session"), errors.New("network unavailable"),
		SecretaryOutputValidationError{Code: "untrusted", Err: errors.New("unknown classification")},
	} {
		assert.Empty(t, SecretaryValidationFailureCode(failure))
	}
}

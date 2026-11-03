package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryReceiptReadErrorGivenExactTypedBadRequestThenMarksEvidenceRetry(t *testing.T) {
	response := apperr.Error{
		Status: http.StatusBadRequest, Message: "invalid read receipt; read the exact source version again",
	}
	for name, failure := range map[string]error{
		"value": response, "pointer": &response,
		"wrapped value":   fmt.Errorf("source read: %w", response),
		"wrapped pointer": fmt.Errorf("source read: %w", &response),
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(
				t,
				secretaryReceiptReadError(failure),
				domain.ErrSecretaryInvalidReadReceipt,
			)
		})
	}
}

func TestSecretaryReceiptReadErrorGivenOtherFailureThenPreservesOriginalError(t *testing.T) {
	message := "invalid read receipt; read the exact source version again"
	for name, failure := range map[string]error{
		"nil": nil, "canceled": context.Canceled, "deadline": context.DeadlineExceeded,
		"owner": domain.ErrUnauthorized, "not found": domain.ErrNotFound,
		"fenced": domain.ErrSecretaryFenced, "conflict": domain.ErrConflict,
		"plain text": errors.New(message), "plain marker": errors.New(domain.ErrSecretaryInvalidReadReceipt.Error()),
		"wrapped text":         fmt.Errorf("source read: %w", errors.New(message)),
		"network":              &net.OpError{Op: "read", Net: "tcp", Err: errors.New(message)},
		"unauthorized":         apperr.Error{Status: http.StatusUnauthorized, Message: message},
		"forbidden":            apperr.Error{Status: http.StatusForbidden, Message: message},
		"not found response":   apperr.Error{Status: http.StatusNotFound, Message: message},
		"server failure":       apperr.Error{Status: http.StatusInternalServerError, Message: message},
		"other bad request":    apperr.Error{Status: http.StatusBadRequest, Message: "invalid read receipt"},
		"prefixed bad request": apperr.Error{Status: http.StatusBadRequest, Message: "source read: " + message},
	} {
		t.Run(name, func(t *testing.T) {
			actual := secretaryReceiptReadError(failure)
			assert.Equal(t, failure, actual)
			assert.NotErrorIs(t, actual, domain.ErrSecretaryInvalidReadReceipt)
		})
	}
}

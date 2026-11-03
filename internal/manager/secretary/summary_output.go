package secretary

import (
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func generatedOutputError(code, message string) error {
	return domain.SecretaryOutputValidationError{Code: code, Err: errors.New(message)}
}

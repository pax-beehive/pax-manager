package domain

import "errors"

// SecretaryOutputValidationError marks rejected generated output, not source or transport failures.
type SecretaryOutputValidationError struct {
	Code string
	Err  error
}

func (e SecretaryOutputValidationError) Error() string { return e.Err.Error() }
func (e SecretaryOutputValidationError) Unwrap() error { return e.Err }

func SecretaryValidationFailureCode(failure error) string {
	if errors.Is(failure, ErrSecretaryInvalidReadReceipt) {
		return "evidence"
	}
	var validation SecretaryOutputValidationError
	if errors.As(failure, &validation) &&
		(validation.Code == "evidence" || validation.Code == "schema") {
		return validation.Code
	}
	return ""
}

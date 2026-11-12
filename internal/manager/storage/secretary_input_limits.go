package storage

import (
	"errors"
	"fmt"
	"strings"
)

const secretaryInputLimitPrefix = "secretary input size requires source change or explicit retry: "

type secretaryInputLimitError struct {
	reason string
}

func (e secretaryInputLimitError) Error() string {
	return secretaryInputLimitPrefix + e.reason
}

func (secretaryInputLimitError) Unwrap() error { return ErrConflict }

func secretaryInputLimit(reason string) error {
	return secretaryInputLimitError{reason: fmt.Sprintf("%s: %v", reason, ErrConflict)}
}

func secretaryInputBlocked(err error) bool {
	var limit secretaryInputLimitError
	return errors.As(err, &limit)
}

func secretaryInputFailureVisible(failure, batch string, blocked bool) bool {
	return blocked && strings.HasPrefix(failure, secretaryInputLimitPrefix) &&
		batch != "running" && batch != "unknown" && batch != "output_received"
}

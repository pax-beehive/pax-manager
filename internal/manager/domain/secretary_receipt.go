package domain

import "errors"

// ErrSecretaryInvalidReadReceipt identifies a rejected generated source proof.
var ErrSecretaryInvalidReadReceipt = errors.New(
	"secretary evidence read receipt requires rereading",
)

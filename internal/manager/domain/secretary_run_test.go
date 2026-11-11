package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretaryRunRequestIDGivenUntrustedInputThenAcceptsOnlyBoundedIdentifiers(t *testing.T) {
	for _, id := range []string{"run-123_a.b:c", strings.Repeat("a", 128)} {
		assert.True(t, ValidSecretaryRunRequestID(id), id)
	}
	for _, id := range []string{"", strings.Repeat("a", 129), "has space", "../path", "unicode\u4e2d", "line\nbreak"} {
		assert.False(t, ValidSecretaryRunRequestID(id), id)
	}
}

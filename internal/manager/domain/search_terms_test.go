package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchTermsGivenWhitespaceAndDuplicatesThenReturnsCanonicalLiteralTerms(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		want        []string
	}{
		{"empty", " \t\n\u0085\u2003", nil},
		{"unordered and duplicate case", " PORT\tapi port\nAPI ", []string{"api", "port"}},
		{"unicode whitespace", "port\u0085api\u2003db", []string{"api", "db", "port"}},
		{"punctuation stays literal", `port_name *?\b "api"`, []string{`"api"`, `*?\b`, "port_name"}},
		{"BOM stays literal", "\ufeffport", []string{"\ufeffport"}},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, SearchTerms(tc.query)) })
	}
}

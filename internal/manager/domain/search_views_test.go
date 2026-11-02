package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchMessageViewGivenRoleThenUsesTheEstablishedDefault(t *testing.T) {
	assert.Equal(t, "full", DefaultSearchMessageView("user"))
	assert.Equal(t, "last", DefaultSearchMessageView("assistant"))
	assert.Equal(t, "full", DefaultSearchMessageView(""))
}

func TestSearchMessageViewGivenParagraphsThenSelectsVisibleTextWithoutChangingSource(t *testing.T) {
	for _, test := range []struct {
		name, body, view, want string
	}{
		{"full preserves whitespace", " \r\nfirst\n\nlast\n ", "full", " \r\nfirst\n\nlast\n "},
		{"first paragraph", " \r\nfirst\n\nlast\n ", "first", "first"},
		{"last paragraph", " \r\nfirst\n\nlast\n ", "last", "last"},
		{"one paragraph", "single\nline", "last", "single\nline"},
		{"unicode last", "first\n\n\u6700\u540e", "last", "\u6700\u540e"},
		{"empty first", " \r\n\t", "first", ""},
		{"empty last", " \r\n\t", "last", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := SearchMessageView(test.body, test.view)
			assert.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
	_, err := SearchMessageView("text", "unknown")
	assert.ErrorIs(t, err, ErrConflict)
}

package domain

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretaryMessageViewGivenLongUserInputThenExposesBoundedUTF8EndsOnly(t *testing.T) {
	body := "User request: review this paste.\n" + strings.Repeat(
		"\u6570\u636e\u5e93",
		40000,
	) + "\nFinal request: keep the useful conclusion."
	originalHash := SearchContentHash(body)
	text, view := SecretaryMessageView(body, "user")
	assert.Equal(t, "head_tail", view)
	assert.LessOrEqual(t, len(text), 32*1024)
	assert.True(t, utf8.ValidString(text))
	assert.True(t, strings.HasPrefix(text, "User request: review this paste."))
	assert.True(t, strings.HasSuffix(text, "Final request: keep the useful conclusion."))
	assert.Contains(t, text, "Middle omitted")
	assert.Equal(t, originalHash, SearchContentHash(body))
}

func TestSecretaryMessageViewGivenOrdinaryInputOrLegacyPolicyThenPreservesEstablishedViews(
	t *testing.T,
) {
	for name, test := range map[string]struct {
		body, role, policy, text, view string
	}{
		"short user":       {"hello", "user", SecretaryInputPolicyVersion, "hello", "full"},
		"threshold user":   {strings.Repeat("x", 32*1024), "user", SecretaryInputPolicyVersion, strings.Repeat("x", 32*1024), "full"},
		"assistant":        {"earlier\n\nfinal", "assistant", SecretaryInputPolicyVersion, "final", "last"},
		"unknown role":     {"text", "system", SecretaryInputPolicyVersion, "text", "full"},
		"legacy assistant": {"earlier\n\nfinal", "assistant", SecretaryLegacyInputPolicyVersion, "final", "last"},
		"missing policy":   {strings.Repeat("x", 40*1024), "user", "", strings.Repeat("x", 40*1024), "full"},
		"explicit legacy":  {strings.Repeat("x", 40*1024), "user", SecretaryLegacyInputPolicyVersion, strings.Repeat("x", 40*1024), "full"},
		"future policy":    {"text", "user", "future", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			text, view := SecretaryMessageViewForPolicy(test.body, test.role, test.policy)
			assert.Equal(t, test.text, text)
			assert.Equal(t, test.view, view)
		})
	}
}

func TestSecretaryMessageQuoteGivenOmittedMiddleThenAcceptsOnlyRealVisibleSpans(t *testing.T) {
	body := "HEAD evidence.\n" + strings.Repeat(
		"x",
		40000,
	) + "HIDDEN MIDDLE" + strings.Repeat(
		"y",
		40000,
	) + "\nTAIL evidence."
	preview, _ := SecretaryMessageView(body, "user")
	require.Contains(t, preview, "Middle omitted")
	assert.True(t, SecretaryMessageContainsQuote(body, "user", "HEAD evidence."))
	assert.True(t, SecretaryMessageContainsQuote(body, "user", "TAIL evidence."))
	for _, quote := range []string{"", "HIDDEN MIDDLE", "Middle omitted", "xxx\n\n[Middle", "HEAD evidence.\nTAIL evidence.", preview} {
		assert.False(t, SecretaryMessageContainsQuote(body, "user", quote), quote)
	}
	assert.True(t, SecretaryMessageContainsQuoteForPolicy(body, "user", "HIDDEN MIDDLE", ""))
	assert.True(t, SecretaryMessageContainsQuote("first\n\nlast", "assistant", "last"))
	assert.False(t, SecretaryMessageContainsQuote("first\n\nlast", "assistant", "first"))
	assert.False(t, SecretaryMessageContainsQuoteForPolicy(body, "user", "HEAD", "future"))
}

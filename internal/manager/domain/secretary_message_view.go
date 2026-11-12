package domain

import (
	"strings"
	"unicode/utf8"
)

const SecretaryUserPreviewBytes = 32 * 1024

const secretaryMiddleOmitted = "\n\n[Middle omitted; read the original message with workspace_read or workspace_expand if needed.]\n\n"

func SecretaryInputPolicySupported(policy string) bool {
	return policy == "" || policy == SecretaryLegacyInputPolicyVersion ||
		policy == SecretaryInputPolicyVersion
}

func SecretaryMessageView(body, role string) (string, string) {
	return SecretaryMessageViewForPolicy(body, role, SecretaryInputPolicyVersion)
}

// Empty policy belongs to batches frozen before selective user previews.
func SecretaryMessageViewForPolicy(body, role, policy string) (string, string) {
	if !SecretaryInputPolicySupported(policy) {
		return "", ""
	}
	if policy == SecretaryInputPolicyVersion && role == "user" &&
		len(body) > SecretaryUserPreviewBytes {
		head, tail := secretaryUserEnds(body)
		return head + secretaryMiddleOmitted + tail, "head_tail"
	}
	view := DefaultSearchMessageView(role)
	text, _ := SearchMessageView(body, view)
	return text, view
}

func SecretaryMessageContainsQuote(body, role, quote string) bool {
	return SecretaryMessageContainsQuoteForPolicy(body, role, quote, SecretaryInputPolicyVersion)
}

func SecretaryMessageContainsQuoteForPolicy(body, role, quote, policy string) bool {
	if quote == "" || !SecretaryInputPolicySupported(policy) {
		return false
	}
	if policy == SecretaryInputPolicyVersion && role == "user" &&
		len(body) > SecretaryUserPreviewBytes {
		head, tail := secretaryUserEnds(body)
		return strings.Contains(head, quote) || strings.Contains(tail, quote)
	}
	text, _ := SecretaryMessageViewForPolicy(body, role, policy)
	return strings.Contains(text, quote)
}

func secretaryUserEnds(body string) (string, string) {
	available := SecretaryUserPreviewBytes - len(secretaryMiddleOmitted)
	headEnd := available / 2
	for headEnd > 0 && !utf8.RuneStart(body[headEnd]) {
		headEnd--
	}
	tailStart := len(body) - (available - available/2)
	for tailStart < len(body) && !utf8.RuneStart(body[tailStart]) {
		tailStart++
	}
	return body[:headEnd], body[tailStart:]
}

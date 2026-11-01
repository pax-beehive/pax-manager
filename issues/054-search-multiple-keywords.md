# ISSUE-054 Search treats space-separated keywords as one phrase

**Status:** resolved
**Severity:** medium
**Component:** search / userapi / MCP
**Found:** 2026-11-01
**Resolved:** 2026-11-01

## Problem

Searching port tcp required that exact contiguous phrase. Separate title/body
occurrences or reversed word order could not match, and the current-source
check and snippet selection also treated the input as one phrase.

## Resolution

Split Unicode whitespace into at most 16 case-insensitively distinct literal
keywords. Rank more matched terms first, then more terms with ASCII identifier
boundaries, then newest. All-keyword results precede partial matches that fill
remaining slots, including when no all-keyword result exists. This is one query
and PIT result stream; pagination never switches between separate searches.

Each distinct literal match contributes N+1 points, and each boundary match adds
one point. This preserves term coverage as the primary tier. Existing owner and
source-visibility checks remain mandatory; current PG source content must still
match at least one term. Snippets locate the earliest matching keyword. Single
terms keep literal substring recall, punctuation escaping and the underscore
boundary rule. No schema, mapping or reindex changes are needed.

Ordinary BDD/testify, stored-source HTTP and real Elasticsearch tests cover
ranking/fill, 20 all-term hits before partial hits across pages, no-all-term
fallback, cross-field and unordered matches, duplication/whitespace, punctuation,
PIT/cursor bounds, current-source changes and ownership. Active API, MCP and
contract descriptions are updated. See
[the handoff](../docs/handoff_20261101_193200.md).

# ISSUE-052 Substring search ignores identifier boundaries and relevance tiers

**Status:** resolved
**Severity:** medium
**Component:** search
**Found:** 2026-11-01
**Resolved:** 2026-11-01

## Problem

Workspace search returned every literal substring match ordered only by time.
A recent transport result could precede an older port result when searching for
port, making the intended identifier hard to find.

## Resolution

Keep title/body continuous-substring recall and add one fixed score bonus when
the literal query has ASCII identifier boundaries in either field. Rank that
single tier first, then order by descending creation time and stable document
and PIT tie breakers. Repeated matches or matches in both fields add no further
bonus. Existing wildcard fields support the query without reindexing.

The boundary rule treats A-Z, a-z, 0-9 and underscore as identifier characters.
Thus port, PORT, port:9879 and (port) precede transport, viewport, port_name and
port123. Non-ASCII characters are delimiters under this explicit technical
identifier policy; it does not provide Chinese segmentation or Unicode
linguistic word boundaries. Literal CJK substring recall remains available.

Versioned query fingerprints reject old pagination cursors. A new cursor carries
four typed sort values bound to the owner, literal query and filters. Existing
owner filtering and source permission checks remain in place. Ordinary unit and
real Elasticsearch tests cover ranking, escaping, boundaries and stable PIT
pagination. See [the handoff](../docs/handoff_20261101_103000.md).

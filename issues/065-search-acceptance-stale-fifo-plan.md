# ISSUE-065 Search acceptance still requires the former FIFO claim index

**Status:** resolved
**Severity:** medium
**Component:** storage / search acceptance
**Found:** 2026-11-03
**Resolved:** 2026-11-03

## Problem

After PR 207 merged, main CI run 41566794274 failed the tied-backlog plan
regression added by PR 208. That test captures the actual ClaimSearchBatch SQL,
but still required idx_search_sync_pending_fifo. The priority scheduler correctly
selected idx_search_sync_pending_priority for its live, history, and extra-live
candidates. The recorded plan had no Sort or Incremental Sort.

## Resolution

Require all three candidate scans to use the ordered priority index. Keep the
Sort and Incremental Sort prohibitions, the real batch-size check, and the
earliest historical row checks. Keep the FIFO catalog and concurrent migration
tests: that index remains part of the supported schema.

The unchanged test was reproduced against isolated PostgreSQL before applying
the assertion fix. No production code, schema, query, or generated artifacts
change. There are no new production statements requiring coverage.

## Verification

- Reproduced the unchanged assertion failure with isolated PostgreSQL 17.
- Passed the complete real-PG index and priority test suites after the fix.
- Passed all 246 selected Go search acceptance checks against isolated PG/ES,
  with no skipped tests, plus all 11 MCP adapter tests.
- Formatting, lint (normal and integration tags), full Go package tests, and
  Manager build passed. The two packages requiring local httptest listeners
  passed after rerunning outside the restricted network sandbox.
- Passed real Manager HTTP and genuine stdio MCP acceptance, including late
  message parts, summary revisions, exact historical source reads, and deletion.
- Passed all 421 Secretary HTTP/ACP/PostgreSQL checks with no skipped tests.
- No production data or running production services were changed.

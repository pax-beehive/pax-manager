# [ISSUE-055] Secretary work sessions flood ordinary session lists

**Status:** resolved
**Severity:** medium
**Component:** storage / session
**Found:** 2026-11-02
**Resolved:** 2026-11-02

## Summary

Automatic summary runs created internal sessions that appeared as ordinary
conversations in the Home sidebar and agent session lists.

## Current behavior

Internal work was excluded from search and Secretary source collection, but
ordinary session-list queries did not apply that exclusion. Recent background
tasks could occupy an entire page and inflate pagination totals.

## Resolution

PostgreSQL and memory session lists exclude the internal source marker, reserved
Secretary session prefix, and durable internal-session registry. The registry
lookup uses both agent and session identity. Filtering occurs before counting,
ordering, and pagination, including legacy agent and current node-agent lists.

No records are deleted or archived. Authorized direct session metadata/history
reads remain available for diagnostics, subject to the existing access checks.
There is no API or database migration. Search behavior is unchanged.

Regression coverage checks all three markers, ordinary similarly named sessions,
registry agent scope, page totals, and authorized versus unauthorized direct
reads. A real PostgreSQL regression exercises the same visibility contract.

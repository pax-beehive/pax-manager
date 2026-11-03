# ISSUE-050 Historical message backfill starves titles and live changes

**Status:** resolved
**Severity:** high
**Component:** storage / search indexing
**Found:** 2026-11-01
**Resolved:** 2026-11-01

## Problem

The durable search queue sorted ready work by available_at, kind and source_id.
Bulk backfill inserts shared an availability timestamp, so message tasks ran
before session titles and summaries. New source changes also waited behind older
historical work. This delayed search visibility despite healthy PG and ES.

## Resolution

Queue rows distinguish backfill from live source changes. Backfill inserts only
missing background rows. Transactional source triggers create or promote live
work, including revision one, while retaining existing revision and lease fences.

The production batch of 20 reserves 16 slots for live changes and four for
history. Each side borrows unused capacity; history selects session titles,
summaries and then messages. A single-slot caller prefers live work and falls
back to history. Retry availability, active leases and stale acknowledgements
retain their existing meaning. Search matching, ACL checks and APIs are unchanged.

A constant default classifies existing queue rows as background without startup
DML or a table rewrite. One new partial ordering index is built once. Repeated
schema application preserves existing live classifications and every queue field.
Real PostgreSQL tests reproduce the old starvation and cover fairness, borrowing,
source triggers, leases, retries, migration and an index-backed claim plan.

This resolution describes the source fix. Deployment is a separate step;
historical message backfill is not asserted complete. ISSUE-049, the separate
production Console search-route gap, remains open.

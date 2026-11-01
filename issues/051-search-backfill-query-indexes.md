# ISSUE-051 Search backfill repeatedly sorts and scans unrelated messages

**Status:** resolved
**Severity:** high
**Component:** storage / search indexing
**Found:** 2026-11-01
**Resolved:** 2026-11-01

## Problem

The current FIFO queue selects by available_at, kind and source_id, but its
pending index covered only available_at. Historical rows share timestamps, so a
small claim could still sort a large pending group. Each source read also checks
replacement displays; the existing general message indexes could scan many
unrelated rows before finding the three supported display message types.

## Resolution

Persist two narrow partial indexes without changing query semantics:

- idx_messages_search_display_agent_session on messages(agent_id, session_id),
  restricted to pax:invocation, pax:invocation_pending and pax:artifact.
- idx_search_sync_pending_fifo on search_sync_state(available_at, kind, source_id),
  restricted to desired_revision > indexed_revision.

Fresh-schema definitions retain all existing indexes. Two standalone SQL files
build the same definitions concurrently on existing databases, outside startup
schema application and outside transaction wrappers. They deliberately fail on
existing names so an invalid or different index cannot be silently skipped.
Operators verify the catalog before skipping a file that has already succeeded.

Real PG tests verify exact index keys, predicates and readiness; index-backed
plans for tied queue claims and replacement lookup; standalone online builds and
fail-on-existing behavior; and unchanged replacement scope and fallback for all
three display types. See the corresponding handoff for rollout and operations
measurements. Backfill completion is tracked separately from this performance
fix. Queue priority changes from PR 207 are outside this issue.

The separately approved production online builds completed with valid and ready
catalog entries. Sampled source execution fell from 335.584ms to 1.365ms and FIFO
candidate execution from 190.602ms to 0.554ms. A subsequent three-minute sample
observed 15.98 message acknowledgements per second, about 4.9 times the earlier
rate, with the existing error count unchanged. Historical backfill remained in
progress; these measurements do not claim complete history or searchable-hit
counts. [Rollout and verification details](../docs/handoff_20261101_080000.md).

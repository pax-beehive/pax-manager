# [ISSUE-030] Concurrent history text flushes reorder chunks

**Status:** resolved
**Severity:** high
**Component:** history
**Found:** 2026-09-17
**Resolved:** 2026-09-17

## Summary

Timer, size-triggered, and completion flushes could extract batches in order but
append them concurrently. A blocked append of "p" could follow a later "ax"
append, producing "axp". Completion could return before an earlier write finished.
The local regression reproduces this mechanism; the precise production incident
has not been traced through Manager receipt and persistence.

## Resolution

Serialize batch extraction, persistence, and failed-batch requeue with one flush
mutex per batcher. Size-triggered writes use the same Flush path. Buffered appends
retain their short pending-map lock. Completion waits for previous writes.
Tests cover overlapping explicit flushes, threshold flushes, completion, and retry
ordering. Existing corrupted history is not repaired by this change. Database
commit errors with ambiguous outcomes remain outside this ordering fix.

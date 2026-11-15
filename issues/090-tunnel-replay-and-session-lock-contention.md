# [ISSUE-090] Internal session alias failures stall tunnel replay and saturate storage

**Status:** in-progress
**Severity:** high
**Component:** ACP transport / session snapshots / storage
**Found:** 2026-11-15
**Resolved:** -

## Summary

Internal transport ID translation reused the user-visible session list, which
excludes Secretary work sessions. Native IDs therefore failed command metadata
updates with `not found` despite the canonical session existing.

The reliable queue producer wrapper hid the durable inbound cursor. Dispatch
failures were recorded but returned as success, allowing replay to repeat a full
batch indefinitely and to advance past failed business writes. Production had
1,010 failed command-list frames in one queue, exceeding its 1,000-frame batch.

Snapshot reconciliation locked every historical session. Dedicated batch session
reports bypassed the per-agent serialization guard, permitting opposite row-lock
orders. Transport receive and completion also acquired queue and journal locks
in opposite orders. Unlimited Manager connections amplified PostgreSQL contention.

## Current note

The isolated fix adds an authorized internal alias lookup, preserves canonical
native bindings, exposes durable replay cursors through the producer wrapper,
propagates dispatch errors, and filters stale replay rows before pagination.
Snapshots lock only current or previously active sessions, share node fence
reads, and serialize batch reports using the same agent guard. Transport state
guards precede journal mutation, and the application connection pool is bounded.

Regression validation and production recovery are in progress. Existing journal
records are preserved. A read-only production audit found only command-list
updates among received frames below applied cursors; no approval or content
message types were present.

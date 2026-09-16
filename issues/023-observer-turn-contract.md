# [ISSUE-023] Observer replay has no consistent turn or message contract

**Status:** resolved
**Severity:** high
**Component:** observer / storage / console
**Found:** 2026-09-16
**Resolved:** 2026-09-16

## Cause

Console supplied after_message_id while Manager read after_seq. Default replay
read the latest session page before selecting the active turn. Replayed durable
text aggregates were then mixed with raw ACP deltas under different IDs. The
raw subscription also began after catch-up, leaving a handoff gap. SessionSeq
is assigned once; updating an existing message does not advance it.

## Resolution

The authenticated user session events endpoint selects or explicitly pins one
business turn. It streams durable message versions, with turn_start/head batch
boundaries and history_remove for superseded display rows. Reconnect refreshes
only that complete turn, including mutable rows before the supplied watermark.
Console replaces the covered turn projection atomically at head. A persisted
turn_done marker lets completed history take ownership back.

The deprecated parameter and invalid cursors are rejected. This applies to the
user observer routes, not encrypted transport or the raw conversation stream.
Manager and Console changes require a coordinated rollout. No production data
has been changed and existing incorrect turn IDs are not rewritten.

## Verification

Tests cover more than 200 rows in one turn, other agent/session/turn exclusion,
same-sequence text updates, cursor validation, idle behavior, completed replay,
live SSE through completion, projection removal, disconnect before head,
reconnect pinning, replacement of fragmented live text, and history handoff.
See the accompanying handoff for commands and repository baseline failures.

# ISSUE-073 Stable historical text waits forever and inflates the visible queue

**Status:** resolved
**Severity:** high
**Component:** Secretary / storage / source eligibility / status
**Found:** 2026-11-07
**Resolved:** 2026-11-07

## Problem

Historical messages can remain `sent` or `received` without a retained successful
terminal event. Even after the source has been idle for days, the strict source
completion gate never prepares a batch. Repeated manual Run cannot create the
missing evidence. The queue label also counts raw dirty revisions, so full
reconciliation displays already processed sessions as waiting for summaries.

## Resolution

Keep reliable completion evidence as a fast path. Otherwise allow canonical
visible text only when the source explicitly reports idle, has no contradictory
active/unknown lifecycle, and its message and text parts have had no writes for
30 seconds. Explicit streaming and pending states remain ineligible. Use real
source write timestamps rather than mutable queue or session metadata timestamps.
Do not alter original message statuses or create synthetic successful terminals.
PostgreSQL finds a valid terminal rather than stopping at an earlier cancellation.

Prepare and publication share source eligibility. Publication still validates
the complete canonical hash/version, ACL, summary revision, lease and settings.
Late parts retain the existing dirty, invalidation and immutable snapshot paths.
Checks use the publication transaction's snapshot, not a claim of native execution
success or a new session lock held during model execution.

The visible pending count now includes genuine canonical changes, removal of
processed inputs, current dependency repair and unfinished current executions.
Raw-only unchanged updates are excluded without acknowledging work or clearing
retry state. Candidates are scoped to dirty message identities and dependency
references instead of scanning every session's full history.

## Validation

Memory and isolated PostgreSQL contracts cover stable history without terminals,
canceled/failed text, recent or split parts, active/unknown runtime, unchanged
metadata, publication version rejection and late-part revisions with old snapshots.
Queue contracts cover raw-only updates, ownership, hidden/replaced inputs, internal
sessions, deletions, nested dependency repairs and unfinished execution recovery.
Full checks and deployment notes are recorded in the accompanying handoff.

This is a code fix requiring normal Manager deployment. No production messages,
checkpoints, summaries or execution statuses were rewritten to validate it.

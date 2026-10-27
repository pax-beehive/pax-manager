# [ISSUE-045] Late artifact publications appear after their turn footer

**Status:** resolved
**Severity:** medium
**Component:** history / Console
**Found:** 2026-10-27
**Resolved:** 2026-10-27

## Cause

An artifact display row can be created after its originating turn has ended.
Sorting by its own first-touch session sequence places it after Done. Display
keys also omitted the turn, allowing reused tool IDs to overwrite another turn.

## Resolution

New artifact displays save the originating tool message's sequence as
`raw_json.source_session_seq`, alongside existing parent and turn IDs. New
turn-scoped display keys and replacement lists keep reused tool IDs separate.
Backend transcript projection and the companion Console page merge use the
source sequence for display while preserving durable pagination cursors.

The fix applies to new artifact writes. Historical-data migration and Mermaid
MIME handling are outside its scope. Existing publication reconciliation remains
in place. Deploy Manager before Console. Deployment is pending.

## BDD verification

Tests cover late registration after turn completion, repeat registration of the
same new upload, reused tool IDs across turns, source tools outside the display
page, and the final rendered artifact/reply/Done order.

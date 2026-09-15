# Message persistence deadlocks with runtime snapshots

**Status:** resolved
**Severity:** high
**Component:** storage
**Found:** 2026-09-15
**Resolved:** 2026-09-15

## Evidence

The operator supplied sanitized PostgreSQL deadlock reports at 04:49:49.014,
04:58:09.357, and 05:05:39.251 UTC on September 15. Runtime snapshot
reconciliation locks agents before agent_sessions. The message upsert CTE can
touch agent_sessions before its messages.agent_id foreign key check locks
agents. The opposite effective lock order creates a cycle and can abort the
synchronous conversation prompt persistence with SQLSTATE 40P01.

The operator reported production at 6de447d. The relevant storage code is
unchanged in a31a1a8; the approval fix is unrelated to this incident.

## Resolution

Snapshot and legacy runtime storage agent mutexes now use FOR NO KEY UPDATE.
This still serializes runtime writers, but is compatible with the KEY SHARE
lock acquired by messages.agent_id foreign key checks, removing the confirmed
cycle without adding message-side locking or retries. ACP no longer persists
session runtime state; paxd snapshots own that projection.

An isolated PostgreSQL 17 concurrency regression invokes the actual
UpsertMessage and ReplaceAgentActiveTurns methods with deterministic ordering.
Both empty and active snapshots pass with the fix. A test-only source overlay
restoring FOR UPDATE reproduces SQLSTATE 40P01 in both cases.

This resolves the diagnosed code defect, not every possible database deadlock.
No production deployment or database change has been performed.

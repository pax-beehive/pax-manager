# [ISSUE-028] Queued turns depend on the originating browser stream

**Status:** resolved
**Severity:** high
**Component:** session / storage
**Found:** 2026-09-16
**Resolved:** 2026-09-16

## Summary

The former process-local queue was consumed only by the normal conversation
HTTP loop. Approval resume, browser disconnect, and enqueue-after-completion
could leave an accepted draft without a consumer. Restart discarded the map.

## Change

Store one slot per agent/session in session_turn_queue. Limit input to 64 KiB;
keep only identity, author, command ID, input, state, and timestamps. The primary
key begins with agent_id for bounded snapshot lookups. No history is retained.

Every applied paxd snapshot wakes a nonblocking, coalesced background check,
including snapshots that repeat idle. Eight checks and 32 detached executions
may run concurrently; each check reads at most 32 rows for its agent. Saturation
waits for the next paxd report. There is no independent Manager polling timer.

Claim with a conditional queue-row UPDATE matching the draft, latest snapshot
sequence/fence, current node fence, and idle session. Network execution occurs
after the statement completes, using the normal ACP pipeline and fixed queued
turn ID. A running/approval snapshot or completed/approval-interrupted runner
confirms receipt and retires the row. No browser response is required.

Ambiguous interruptions are retained as uncertain and never automatically
replayed. A sending slot left by a vanished process becomes uncertain after
two minutes when a subsequent snapshot is processed. Users can inspect the
conversation and explicitly remove that record. Removing it does not cancel an
already delivered prompt. Pending queued rows survive restart.

## Verification

Passed make fmt-check, make lint (including integration-tag lint), full Go tests,
Manager build, focused queue tests, and isolated PostgreSQL/pgx SQL checks.
Console TypeScript, scoped ESLint, and 16 relevant tests passed. Docker-backed
end-to-end tests were not run. No deployment performed.

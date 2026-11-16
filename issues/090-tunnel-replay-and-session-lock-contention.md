# [ISSUE-090] Internal session alias failures stall tunnel replay and saturate storage

**Status:** resolved
**Severity:** high
**Component:** ACP transport / session snapshots / storage
**Found:** 2026-11-15
**Resolved:** 2026-11-15

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

## Resolution

The isolated fix adds an authorized internal alias lookup, preserves canonical
native bindings, exposes durable replay cursors through the producer wrapper,
propagates dispatch errors, and filters stale replay rows before pagination.
Snapshots lock only current or previously active sessions, share node fence
reads, and serialize batch reports using the same agent guard. Transport state
guards precede journal mutation, and the application connection pool is bounded.

Full unit, lint, format, build, isolated integration, real PostgreSQL lock,
and targeted race checks passed. Changed-statement coverage is 96.2%.
The US hotfix was deployed from `66896f2` at 21:15 UTC. Eighteen ACP tunnels
recovered; the first four-minute observation had no new deadlocks and no recent
ACP handling errors. One-minute host load fell from about 60 to 6.72. PostgreSQL
CPU remained elevated while writes and vacuum continued; this fix does not
establish an idle-load baseline. Old containers and journal records are retained.

A read-only production audit found only command-list
updates among received frames below applied cursors; no approval or content
message types were present.

Review drafts: [Manager #255](https://github.com/pax-beehive/pax-manager/pull/255)
and [Paxkit #11](https://github.com/pax-beehive/paxkit/pull/11). See the
[handoff](../docs/handoff_20261115_210700.md) for build and rollback details.

## Notification ordering follow-up

Longer rollout observation found a new native-only command update preceding its
session/new reply. Strict retry blocks the later reply needed to bind that
session. The follow-up deployed from `11190d3` durably defers catalogs, recovers creation
correlation after restart, and hydrates after binding. The actual stuck queue
advanced from applied sequence 28 to 30; its catalog matched the original payload
in the canonical session. Unit, PostgreSQL, race, and the new real HTTP/ACP
early-notification regression passed. The initial replay CPU loop and storage
deadlocks remain fixed.

## Paxd follow-up

The installed clients also need the shared dependency update and client-specific
worker/reconnect fixes. [Paxd #122](https://github.com/pax-beehive/paxd/pull/122)
and [paxkit #12](https://github.com/pax-beehive/paxkit/pull/12) contain that work.
The actual compiled daemon passed a forced-reconnect test against real Manager
and PostgreSQL, including command catalog binding, responses, a subsequent
prompt, persistent SQLite ID binding, and one invocation per request. This
acceptance does not mean installed clients have been upgraded. See the
[client handoff](../docs/handoff_20261116_001400.md) for coverage, artifacts,
existing output durability limitations, and preserved Mac permission history.

## Final shared dependency

Manager now pins the final shared replay dependency
`github.com/pax-beehive/paxkit v0.0.0-20261116221549-4d630f079c06`.
Dependency checksums were refreshed with Go 1.25.8; a second
`go mod tidy -diff` reports no changes.

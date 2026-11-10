# ISSUE-074 Secretary canceled executions remain unknown

**Status:** partial
**Severity:** high
**Component:** Secretary / ACP terminal projection / recovery
**Found:** 2026-11-10
**Resolved:** -

## Problem

Two production Secretary attempts reached the worker's five-minute execution
deadline. The native DSCODE streams each contain one matching turn start and
one turn end with `reason.kind=aborted`, with interrupted final assistant state
and no later turn. They ended at 2026-11-10 01:05:16.348Z and 02:41:28.002Z.
Manager still records their attempts as unknown and blocks those source tasks.

The native-to-Manager session mapping and business turn identifiers match.
Actual Manager history has 8 and 15 rows respectively, but neither has a
`turn_done` or other durable terminal record. The session row's cached message
count is zero and must not be used to conclude that no messages exist.

The current ACP history classifier retains `end_turn` but flushes and discards
RPC errors and other stop reasons. Snapshot-authoritative runtime metadata now
reports idle without a stop reason or a completed prompt/turn identifier.
Idle alone cannot prove completion or failure. The durable transport journal
was not exhaustively searched; this report does not assert that the original
terminal frame is absent there.

## Required behavior

- Persist a terminal record at the reliable inbox projection boundary only
  when an RPC response has trusted request, session, owner, agent and sealed
  business-turn correlation to the original Secretary attempt.
- Make that projection idempotent. Preserve a fixed terminal classification,
  including known cancellation/failure, without copying raw errors or source
  content into diagnostic output.
- Recover only the exact original attempt. A known failure may enter the
  bounded failure/retry policy; an unconfirmed execution must remain unknown.
- Never turn a timeout, idle snapshot, or cancellation send into a successful
  `turn_done`. Do not automatically replay unknown native work.
- Verify whether every supported Paxd runtime seals terminal RPC metadata;
  missing correlation requires a protocol fix rather than a heuristic.

## Current note

The current node-path implementation adds a durable Manager-owned dispatch
association and an idempotent failure record projected from the reliable inbox.
Strict request/session/turn/batch/attempt matching supplies the known-failure
classification. A scoped recovery wake and a proof check under the task lock
cover terminal-before-timeout and terminal-after-timeout ordering without a
full source scan. Bounded failure retries preserve pauses and current settings.
Full Go tests, isolated PostgreSQL contracts, HTTP/ACP acceptance, the isolated
Docker integration suite, build and lint passed. Final review found no blocker.
The implementation has not been deployed. Runtime sealing compatibility and
production acceptance remain rollout checks; preexisting unknown executions
are not automatically reset.

## Operational boundary

The two native-aborted attempts can be retried individually after verifying
their exact identities and no active lease, through the existing task-control
store with CAS and explicit confirmed restart. This is an operational repair,
not a general automatic terminal reconciliation fix. Other pending tasks and
checkpoints must not be reset.

The separate multi-summary publication budget and safe validation feedback
changes are tracked in [issue 067](067-secretary-topic-capacity-and-execution-lifecycle.md).

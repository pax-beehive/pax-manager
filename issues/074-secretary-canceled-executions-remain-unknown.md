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
The implementation was confirmed deployed in production Manager e6ae940 on
2026-11-11. A preexisting unknown attempt still has neither the prepared request
association nor a terminal record required by the new recovery proof. Such old
executions are not automatically reset. Runtime sealing compatibility and
complete production recovery acceptance remain separate checks.

The 2026-11-11 follow-up found a production wiring gap: `newServer` wraps the
storage implementation in `canonicalSessionStore`, which did not forward the
optional terminal-recovery wake. A verified cancellation persisted about
0.7 seconds after an attempt became unknown, but the wrapper silently prevented
its recovery wake. The fix preserves the named wake interface through the
wrapper and propagates wake errors so reliable delivery can retry projection.
Regression coverage must exercise the actual service wrapper, not just raw
storage or a replacement wake mock. This does not change the five-minute
execution deadline or classify an unconfirmed timeout as a known failure.

Terminals already acknowledged before this fix will not be delivered again
on deployment. For those tasks, use the existing per-task `recheck` action
with the observed task revision, batch, attempt, retry epoch and settings
revision. It reads the
original attempt only, does not dispatch a prompt, and applies the existing
bounded retry policy only after a verified failure. Missing proof remains
unknown. Changed settings or source versions can return a conflict; inspect
the current task instead of bypassing that guard. Do not reset all tasks or
force a new epoch as a recovery shortcut.

## Operational boundary

On 2026-11-13, a bounded read-only audit found three other historical unknown
attempts with exact persisted cancellation proofs, current settings/bindings,
internal-session registration and no active lease. Calling the existing
WakeSecretaryExecutionRecovery for those exact attempts released their blocked
flags without changing checkpoints, retry epochs or settings. Normal original-
attempt recovery then recognized the cancellations and entered bounded retries.
One source subsequently committed a new batch. This is a targeted operational
repair, not proof that all historical tasks have completed. Separately, a
persisted successful native response was ignored by the reader; issue 088
tracks that different cause.

The two native-aborted attempts can be retried individually after verifying
their exact identities and no active lease, through the existing task-control
store with CAS and explicit confirmed restart. This is an operational repair,
not a general automatic terminal reconciliation fix. Other pending tasks and
checkpoints must not be reset.

The separate multi-summary publication budget and safe validation feedback
changes are tracked in [issue 067](067-secretary-topic-capacity-and-execution-lifecycle.md).

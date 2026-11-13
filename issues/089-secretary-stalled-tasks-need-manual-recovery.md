# ISSUE-089 Stalled Secretary tasks wait indefinitely for manual recovery

**Status:** partial
**Severity:** high
**Component:** Secretary / execution lifecycle / recovery
**Found:** 2026-11-13
**Resolved:** -

## Problem

Secretary tasks repeatedly stopped in states that only an owner action could
leave. Any failure outside a short allowlist became an unknown outcome, and an
unknown outcome blocked its source until a terminal proof arrived or the owner
used recheck or retry. A batch blocked after three rejected outputs, or after
exhausted connection retries, waited the same way. Issues 063, 074, 087 and 088
each fixed one cause; a cause outside those lists still stalled the task.

Three execution paths widened the problem:

- Session creation, tool registration, runner claim and the pre-prompt checks
  all precede session/prompt. Only two tunnel errors and one structured slot
  error were classified as not dispatched; every other failure there became an
  unknown outcome although no native turn could exist.
- The per-second fence monitor canceled a running execution on any lookup
  error, so one storage hiccup produced an unknown outcome and could discard a
  result that had already arrived.
- The native turn was canceled only when the worker context had ended. After a
  stream error or a pending approval it could keep the agent slot busy, so
  later attempts failed to connect until their retry budget was exhausted.

## Current note

The change is implemented and locally verified; production acceptance is a
separate gate, so this stays partial.

- The conversation runner records when a session/prompt frame is handed to the
  transport. Every failure before that point is classified as not dispatched
  and uses the existing bounded connection retry. Fencing keeps its own
  meaning. An explicitly unknown outcome stays unknown. A failed write is
  treated as dispatched, because it may still have reached the transport.
- The fence monitor cancels an execution only for a definitive fence. A failed
  lookup is reported once per execution and the execution continues;
  publication repeats every fence check.
- A dispatched turn that does not finish cleanly is always asked to stop,
  including after a stream error and when it waits for an approval.
- Stalled tasks restart automatically. A task qualifies when its job cannot be
  claimed, its current batch is unknown, blocked, or pending behind a blocked
  job, it has an attempt, it is enabled, not paused, not leased, and not
  blocked by the deterministic input size limit. The restart is the same
  transition as an explicit owner retry: a new retry epoch, with the old batch
  and attempt fenced so a late result cannot commit. The wait is 15 minutes,
  then 1 hour, then 4 hours, measured from the start of an unknown attempt,
  otherwise from the last failure of the batch. At most three consecutive
  restarts are made. Publishing or any explicit owner retry starts a new
  budget.

A restart may spend another model call on the owner's own agent. That is the
reason for the small budget and the growing wait. A task that exhausts the
budget still needs an owner action, and the task view reports its state as
before.

## Not covered

- Issue 064: a source whose upstream runtime snapshot stays running is still
  not claimed. That needs authoritative runtime state, not a retry.
- The Console does not yet show when the next automatic restart is due.
- The single sequential worker is unchanged. One slow execution still delays
  other sources.
- A restart, like the explicit task retry, leaves the replaced batch in its
  old epoch as unknown. The bulk run command still skips a source that has any
  unresolved batch, so such a source needs the per-task retry or the automatic
  restart rather than the bulk command.
- Existing production tasks are not rewritten by deployment. Tasks that are
  already unknown or blocked become eligible under the same rules once their
  wait has elapsed.

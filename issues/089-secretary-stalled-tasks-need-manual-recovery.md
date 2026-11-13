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

The change is implemented and verified locally and in CI; production acceptance
is a separate gate, so this stays partial.

- The conversation runner records when a session/prompt frame is handed to the
  transport. Every failure before that point is classified as not dispatched
  and uses the existing bounded connection retry. Fencing keeps its own
  meaning. An explicitly unknown outcome stays unknown. A failed write is
  treated as dispatched, because it may still have reached the transport.
  session/new is bounded to two minutes for an internal execution.
- The fence monitor cancels an execution only for a definitive fence. A failed
  lookup is reported once per execution and the execution continues;
  publication repeats every fence check.
- A dispatched turn that does not finish cleanly is always asked to stop,
  including after a stream error and when it waits for an approval.
- Stalled tasks recover automatically. A task is stalled when its current batch
  has an attempt and ordinary claims skip the source for a reason that does not
  clear by itself: the job is blocked, the batch exhausted its connection
  retries, or a guided operation needs attention. It must be enabled, not
  paused, not leased, and not blocked by the deterministic input size limit.
  - An unknown, interrupted, blocked or stuck pending batch is restarted. The
    restart is the same transition as an explicit owner retry: a new retry
    epoch, with the old batch and attempt fenced so a late result cannot
    commit.
  - Saved output behind a blocked job is released for publishing, at most once
    per saved output. Nothing is discarded and no model call is made.
  - The wait is 15 minutes, then 1 hour, then 4 hours, measured from the start
    of an unknown or interrupted attempt, otherwise from the last failure.
  - A batch that already dispatched two or three attempts gets one restart; a
    batch that dispatched at most one gets three. Publishing, a settings
    change, an explicit owner retry, or a source change that replaces the
    frozen batch starts a new budget.

A restart spends model calls on the owner's own agent, which is the reason for
the small budget and the growing wait. One stalled task costs at most five
extra model calls (1 + 1 + 3 across three restarts), and usually one. A task that exhausts the budget still needs
an owner action, and the task view reports its state as before.

## Known behavior to keep in mind

- Secretary runs unattended and does not wait for a tool approval. A turn that
  asks for one is stopped, so that approval can no longer be granted, and the
  attempt follows the bounded retry. An agent that always asks ends blocked
  after the budget above instead of holding its slot indefinitely.
- An attempt that is already older than its wait when it is first classified,
  for example after a long outage, restarts at the next check without a further
  wait for a terminal proof.

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
  already stalled become eligible under the same rules once their wait has
  elapsed.

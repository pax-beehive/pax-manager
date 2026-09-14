# [ISSUE-018] Detached permission requests have no actionable approval ID

**Status:** resolved
**Severity:** high
**Component:** session
**Found:** 2026-09-13
**Resolved:** 2026-09-13

## Summary

Manual approval persistence depended on the initiating conversation SSE reader.
After it disconnected, permission frames could reach history and observers
without an approval ID, leaving Allow disabled while runtime waited for approval.

## Resolution

Persist manual requests in the agent ACP pipeline before broadcasting them.
Reuse that approval in conversation streams and preserve its ID in runtime state.
Console decision pending state applies only to the matching approval card.
The companion paxd change replies with cancelled outcomes to pending permissions
after forwarding session/cancel, allowing cooperative agents to complete the turn.

## Verification and rollout

Manager full tests and native build pass; the change introduces no new lint
issues. Companion paxd runtime and Console permission card tests pass. Existing
repository-wide formatting and lint failures are recorded in the handoff.
This fixes the current ACP conversation/observer pipeline, not the legacy
mailbox protocol. It has not been deployed and does not backfill approvals
missing from earlier requests.

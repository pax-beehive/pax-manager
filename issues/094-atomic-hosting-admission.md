# [ISSUE-094] Gateway admission repeats policy and budget checks before generation

**Status:** resolved
**Severity:** medium
**Component:** hosting / gateway accounting
**Found:** 2026-11-19
**Resolved:** 2026-11-19

## Summary

The independent Gateway needs several Manager calls to authenticate, select a
model, reserve budgets, persist moderation, and begin generation. The final begin
step also rejects already admitted work after a lease, policy, or balance change.

## Resolution

The current service-authenticated accounting endpoint accepts `prepare` and
`finish`. Prepare performs one serialized admission against the authoritative
policy, execution lease, user quota, and platform pool. It validates Chat
reasoning capabilities before creating a hold. Finish records moderation and
actual usage once without repeating admission checks, including when hosting is
subsequently disabled.

Atomic reservations cannot use the legacy moderation, begin, cancel, or settle
actions. The old actions remain available for existing Gateway deployments.
Abandoned atomic reservations are conservatively charged, never assumed unused.
A first late finish can correct that provisional charge without changing a new
month's balances. Unknown usage is explicitly marked for reconciliation.

This resolves Manager accounting for the independent Gateway. Gateway rollout
and any explicit cancellation control endpoint are separate work. No legacy
agent protocol behavior or production infrastructure changed here.

## Verification

Tests exercise all three generation protocols, trusted model capability checks,
concurrent admission, revoked leases, policy changes, budget overshoot,
moderation rejection, duplicate finish, crash reconciliation, and month changes.
HTTP tests also check service authentication, bounded usage evidence, malformed
reasoning, and settlement after hosting is disabled. See the associated handoff
for the complete local checks.

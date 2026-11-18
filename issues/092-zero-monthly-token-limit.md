# [ISSUE-092] Zero monthly token caps reject funded model requests

**Status:** resolved
**Severity:** medium
**Component:** hosting / configuration / model accounting
**Found:** 2026-11-17
**Resolved:** 2026-11-17

## Summary

An operator setting a monthly token cap to zero could not use monetary budgets
alone: wake, reservation and generation treated zero as token exhaustion.

## Resolution

Zero now means unlimited monthly tokens for configured grants, administrator
defaults and per-user overrides. Spent and reserved tokens remain recorded.
Positive token caps still apply. Dollar reservations, per-user dollar limits,
the monthly operator pool, policy checks and live leases remain mandatory.

New deployments default to unlimited monthly tokens and USD5 per user. The pool
still defaults to zero, which pauses model admission. Explicitly invalid money
grant values cannot be mistaken for an unconfigured all-zero Config.

No schema migration or balance rewrite is required. Existing default changes
retain the current-month grant contract; per-user overrides apply immediately.
Use the separate access pause control to suspend a user. This issue records the
code fix; production publication is pending.

## Verification

Regression coverage includes configuration validation, all three grant sources,
generation admission after a limit change, token accounting, user and pool money
exhaustion, restart preservation and UTC monthly rollover.

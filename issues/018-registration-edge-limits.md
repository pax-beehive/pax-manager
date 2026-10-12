# Registration edge limits

- Status: partial
- Resolved: -
- Severity: high
- Component: edge / registration
- Tracking: KEV-37

## Problem

Public registration needs edge flood protection without blocking headless setup,
polling or reconnects.

## Resolution

Applied and verified exact-path registration-start burst protection using the
available Free-plan rule. Observed 429, Retry-After: 10, recovery and sampled
edge-block events. Managed WAF remains active and no challenge was introduced.

## Current note

This does not satisfy the original four independent edge limits or the exact
5/minute target. Further limits, retry improvements and broad load evidence
remain. See [handoff](../docs/handoff_20260911_042500.md).

# Regional paxl login races (KEV-90)

- Status: partial
- Resolved: -
- Severity: high
- Component: auth / storage / Worker / paxl / Console
- Linear: KEV-90

## Problem

An unauthenticated CLI does not know its owner's region. Starting requests in
both regions must not turn two approvals, two emails, retries, or client crashes
into credentials issued in two regions for the same coordinated login attempt.

## Current note

Implemented a versioned confirm/commit/ack protocol, local durable identity and
region selection, atomic regional issuance, signed read-only home routing and
Console integration. No D1 additions. Legacy clients retain existing behavior
with atomic issuance; only the new protocol has recoverable client coordination.
Copied client state or independent attempts do not share a global arbiter.
Release and live HK verification remain outstanding. See
[handoff](../docs/handoff_20261005_164500.md) for scenarios, checks and rollout.

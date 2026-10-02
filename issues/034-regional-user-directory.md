# Regional user directory and Worker-owned provisioning

- Status: partial
- Resolved: -
- Severity: high
- Component: auth / storage / Worker
- Linear: KEV-75

## Problem

Independent Manager implicit user creation can create two IDs and two business
accounts for the same authenticated identity across US and HK.

## Current note

Implemented D1 immutable identity-to-ID/region assignments, bookmark-aware reads,
10-second signed bootstrap state, Worker-orchestrated provisioning, and the
protected idempotent Manager endpoint. Regional mode removes implicit creation
from both ordinary authentication and static registration-owner authentication.
Legacy mode and existing credentials remain compatible. Import tooling preserves
existing IDs. Cross-region duplicate identities now automatically prefer the
existing US account, with a private decision report and no user-facing conflict.
Existing D1 assignments always take precedence; HK-only identities remain HK.
Inconsistent single-region inventories and IDs reused for unrelated identities
remain operator-only preflight failures. No regional business data is merged.

Production D1 schema and initial user import are complete: five assignments
(US four, HK one), with existing IDs preserved. Both Managers and the Worker
are deployed. Browser routing, runtime probes and Console bootstrap integration
are implemented in the follow-up, pending coordinated live activation checks.
Machine credentials and paxd/installer integration remain KEV-76/77/78 work.

Worker coverage, real workerd D1/SSE/WebSocket tests and the Console checks pass.
The original merged change also passed all three GitHub CI jobs, including the
full integration suite. See the browser routing handoff for current activation
and rollback boundaries.

See `workers/region-directory/README.md` for the API, rollout boundary and tests.

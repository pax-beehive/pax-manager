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

Production D1 creation/import, regional configuration, Access service-auth setup,
coordinated activation and KEV-76/77/78 client integration remain outstanding.
The user has now authorized merging and staged deployment. A D1 database exists;
production inventory export/import is awaiting explicit authorization after an
automatic approval rejection. `BOOTSTRAP_ENABLED=false` prevents a staged Worker
from assigning identities before Console and Manager activation are ready.

Unit, coverage, lint, build, real PostgreSQL concurrency and Manager API integration
checks pass. The full Docker suite has a paxd chat round-trip timeout that also
reproduces on unchanged base `565b3a8`; see the handoff for the comparison.

See `workers/region-directory/README.md` for the API, rollout boundary and tests.

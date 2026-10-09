# Focused customer analytics

## Behavior

GET `/api/v1/user/self/customer-analytics` returns metadata for all regional
accounts only after current administrator authorization. The local test account
is excluded. PostgreSQL pre-aggregates relations to avoid count multiplication.
Real agents must have a heartbeat and a paxd node; active counts exclude deleted
agents and nodes, while the first known binding retains soft-deleted history.
Confirmed sends count only user-role, user-to-agent messages. Encrypted records
remain a separate activity signal. No content or credentials are returned.

The regional Worker handles the same path under `/api/pax`, authenticates and
checks origin before fan-out, and forwards the original identity to fixed US/HK
Managers. Both must authorize the administrator. It makes no D1 lookup and does
not provision accounts for this read. Regional failures remain explicit; the
Console retains earlier snapshots and labels them stale. There is no scheduled
aggregation job. Each Manager keeps a ten-second in-process cache after auth,
with one refresh at a time and a five-second database deadline.

POST `/api/v1/user/self/customer-visit` records server time for the authenticated
account only, with monotonic last-visit updates. `customer_visits` is an additive
table created by the existing startup schema flow. No old timestamps are
backfilled as visits. The signed-in Console submits once per minute at most,
only while visible and focused. Anonymous traffic is not tracked.

## Modules and contracts

- `domain/customer_analytics.go`: metadata and optional persistence interface.
- `storage/customer_analytics.go`: aggregate SQL and authenticated visit upsert.
- `customer_analytics.go`: authorization, metadata cache, HTTP handlers.
- `workers/region-directory/src/customer-analytics.ts`: guarded regional reads.
- Companion Console branch: `feat/customer-dashboard-focus`.
- Dashboard polling: 15 seconds when focused; no hidden/blurred/offline polls,
  overlapping requests, catch-up bursts, or default automatic Query triggers.

## Validation

- Handler unit tests cover unauthorized/non-admin/admin/cache/error paths.
- Isolated PostgreSQL fixtures cover real versus paxl/deleted/unseen bindings,
  user versus assistant/encrypted messages, null owners, and visit monotonicity.
  Run with `PAX_MANAGER_ANALYTICS_TEST_DATABASE_URL` pointing only at a disposable
  test database. This test creates and removes its own uniquely named schema.
- New handler and storage functions: 100% statement coverage in targeted runs.
- Full Manager Go suite, formatting, lint, and binary build passed.
- Worker full suite: 94 tests, 98%+ overall coverage, typecheck, local dry build.
- Console full suite: 854 passed, two skipped; focused polling and browser
  fixtures cover two minutes unfocused with zero new requests and one refresh
  on return. No production customer data is used in frontend fixtures.

## Rollout

Deploy compatible Managers in both regions first, then the Worker using the
currently active browser configuration, then Console. Preserve existing Access
and regional routing secrets. No new credentials are needed. The Worker dry-run
configuration has bootstrap disabled; do not use that to overwrite production.
Follow the existing runbook and back up PostgreSQL before the additive schema
startup. Rolling back application binaries may leave the unused visit table.
Do not drop it during rollback. No deployment has been performed by this change.

## Limits

The dashboard's non-admin filter is explicitly a permission filter, not a claim
that every admin is internal or every normal account is a paying customer.
Encrypted history does not expose roles; precise encrypted send totals and
historical visits cannot be reconstructed. Visits are focused browser presence,
not HTTP hits or proof of human interaction. A page already open in an old
Console will not start visit collection until it loads this release.

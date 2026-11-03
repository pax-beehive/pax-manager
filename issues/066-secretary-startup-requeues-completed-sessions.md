# ISSUE-066 Secretary startup requeues previously processed sessions

**Status:** resolved
**Severity:** medium
**Component:** storage / Secretary bootstrap
**Found:** 2026-11-03
**Resolved:** 2026-11-03

## Problem

Every Secretary worker startup runs the same full reconciliation used by explicit
settings changes and manual retry. It advances every enabled source's desired
revision and rewrites message change revisions, including sessions with valid
summaries and checkpoints. The status page therefore shows a large pending queue
after an ordinary Manager deployment.

Unchanged canonical content is acknowledged without a model call, but the
unnecessary reads, queue transitions, and scheduling changes still occur. This
queue is separate from Elasticsearch indexing and search group projection.

## Required behavior

Ordinary startup must preserve completed, pending, leased, and deferred retry
state while seeding genuinely missing work. Re-enabling Secretary and explicit
retry must retain recovery for changes made while disabled. Legacy summary
migration, current ownership checks, immutable summaries, and checkpoint fences
must remain intact.

## Resolution

Startup now selects missing work and compatibility repairs before advancing any
job revisions. Ordinary completed, pending, leased, and deferred jobs retain all
queue and checkpoint fields. Startup transactions serialize with an advisory
lock and explicit READ COMMITTED isolation, so concurrent workers seed once.

Explicit enable and manual retry retain full reconciliation, including tombstones
for source messages deleted while disabled. These existing transactions do not
take the startup advisory lock, preserving their job/settings lock order.

Real PostgreSQL regressions reproduce the old failure and cover restart state,
missing work, concurrent startup, owner fencing, canonicalization repair, stable
head identity, legacy migration, and disabled final-message deletion. Full Go
tests, formatting, lint, build, and 448 Secretary acceptance checks passed. New
bootstrap Go functions have 100% unit statement coverage.

Production queue rows and summaries were not manually reset or acknowledged.

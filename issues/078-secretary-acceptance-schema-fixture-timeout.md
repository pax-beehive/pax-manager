# ISSUE-078 Secretary acceptance schema fixtures exceed the package timeout

**Status:** partial
**Severity:** high
**Component:** CI / Secretary acceptance fixtures
**Found:** 2026-11-10
**Resolved:** -

## Problem

Main CI run 42913861251 passed the normal Docker integration suite and search
acceptance, then exceeded the storage package's 300-second Secretary acceptance
timeout. The image publication job was skipped because integration is a required
dependency; image compilation was not the failing step.

The storage package completed 205 top-level tests before its deadline. The
reported terminal-wake subtest had started only about 85 milliseconds earlier
and was building its PostgreSQL fixture. It was not blocked for five minutes.
Each fixture creates and drops a full isolated schema using the four production
schema files. On the CI runner, 131 completed PostgreSQL leaf cases had a median
duration of 1.46 seconds. Their cumulative fixture-heavy test cost exhausted the package
budget as the regression suite grew.

## Required behavior

- Run the complete existing Secretary acceptance selection and no-skips gate.
- Preserve the 300-second package timeout and PostgreSQL durability defaults.
- Keep the database disposable and isolated from normal development data.
- Keep image publication gated on successful checks and integration.

## Current note

The acceptance container mounts its PostgreSQL 16 data directory on a 4 GiB
tmpfs. This reduces disk-backed volume I/O for repeatedly rebuilt schema
fixtures. The ceiling is not a preallocation. Container naming, loopback port
allocation and cleanup are unchanged; no application or deployment code changes.

A controlled 18-scenario terminal-proof comparison passed with both settings:
default storage took 24.287 seconds and tmpfs took 19.009 seconds. PostgreSQL
fsync, full_page_writes and synchronous_commit remained enabled. The exact full
local `make secretary-acceptance` passed 1342 named checks with no skipped tests;
the storage package completed in 225.234 seconds. This is not a same-runner
comparison with CI. GitHub verification and main image publication remain
pending.

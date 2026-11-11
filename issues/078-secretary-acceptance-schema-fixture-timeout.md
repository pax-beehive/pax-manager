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

The previous tmpfs mitigation passed main CI once, but main run 42973018519
again exceeded the storage package's 300-second limit. It completed 223
top-level tests without a named-test failure. The final case had run for only
0.541 seconds and was executing schema DDL. The job took about 11 minutes
because core integration, search acceptance and Secretary acceptance ran
serially.

The disposable acceptance container now opts into a pristine PostgreSQL
template database. Production DDL is applied once, then each case clones a
separate database and inserts fresh seed data. Case pools close before their
databases are dropped. The template lives only for the disposable container's
lifetime. Ordinary local fixtures still use independent schemas and do not
require database-creation privileges. The 4 GiB tmpfs, durability defaults,
full test selection, no-skips gate and 300-second package limit are retained.

Regression cases check DDL and data isolation, fresh seed timestamps, template
contents, cleanup and template-name validation. A same-runner comparison of the
exact 18-scenario terminal-proof selection, including fresh template setup,
passed in 19.243 seconds before and 5.049 seconds after (73.8% less time).

CI runs core integration, search acceptance and Secretary acceptance on separate
runners. The existing `integration tests` check aggregates all three and fails
unless each succeeds; image and Worker publication still require that gate.
Secretary diagnostics include PostgreSQL logs, and successful acceptance logs
report package timings. The exact full local `make secretary-acceptance` passed
1355 named checks with zero skips; storage completed in 40.388 seconds, Manager
in 9.568 seconds, userapi in 1.193 seconds and the worker in 0.433 seconds.
GitHub verification is pending; the local full run is not a same-runner
comparison with the 300-second failed CI run.

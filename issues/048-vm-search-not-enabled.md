# ISSUE-048 VM deployment leaves workspace search disabled

**Status:** resolved
**Severity:** high
**Component:** deployment / search
**Found:** 2026-10-31
**Resolved:** 2026-10-31

## Problem

The US Manager had no SEARCH_URL or ES container, so keyword search could not
operate. A live probe also found that its restored old binary lacked the route
and returned 404. Current search-enabled Manager source returns 503 when its
backend is unconfigured; PG-backed persistence is independent of ES, without a
PostgreSQL keyword-search fallback.

The existing VM helper created a private persistent ES container but did not
wait for readiness or repair existing container network membership. Deployment
instructions described only the historical GCloud env-file launcher. The current
direct Release executor copies the active Manager's environment and networks;
editing the historical env file alone cannot enable its search backend.

## Resolution

The source change adds bounded ES readiness and a controlled first-activation
tool using the current immutable Manager image, private checkpoints and exact
container rollback. Unit tests cover success, failure, interrupted recovery,
network and static-IP preservation, release locking and foreign-container refusal.
CI runs the new helper suites with an 80 percent activation coverage threshold.
All 13 shell-helper tests and 27 activation tests passed; activation statement
coverage was 99.65 percent. Real Docker checks proved durable ES data, runtime
preservation, explicit rollback and automatic restoration after failure.

US activation then revealed that the rollback executable lacked search routes,
despite matching init.sql files. Normal Release task
dep_eb5c8135689d4df0904f1c461492dbea successfully deployed the existing registered
search-enabled image while inheriting the new search settings and all networks.
Manager health returned 200 and unauthenticated search returned 401. ES was green
without published ports; indexed documents increased from 60 to 260, with zero
sync errors across about 230,000 historical sources. Backfill continues in the
background, so historical search coverage is initially partial.

The separate production Console /search route still returned 404; ISSUE-049
tracks that frontend deployment gap.

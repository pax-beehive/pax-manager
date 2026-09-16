# [ISSUE-027] CI fails formatting, lint, and object storage startup

**Status:** resolved
**Severity:** high
**Component:** deployment
**Found:** 2026-09-16
**Resolved:** 2026-09-16

## Summary

CI run 35148029374 fails formatting before unit tests and cannot pull the
Docker Hub MinIO image for integration tests. Full local lint also reports
an unchecked rows close and eight functions above the complexity limit.

## Change

Apply the repository formatter to non-generated Go sources. Extract existing
acknowledgement handling, maintenance validation, session creator resolution,
runtime fence checks, and snapshot reconciliation phases into helpers.
Preserve transaction boundaries and lock order.

Use the same pinned MinIO server and client releases from quay.io in all
three Compose configurations. Both replacement manifests were verified
without starting a local Docker daemon. Keep all CI checks enabled.

## Integration contract

After restoring image pulls, run 35149278588 exposed an obsolete ACP runtime
test: it expected ACP events to persist session state. Runtime snapshots have
been authoritative since issue 019. The test now sends node-control snapshots
for running, approval, resume, and idle, using the prompt envelope turn ID.
It also checks that ACP permission and completion frames do not overwrite
the last snapshot.

## Verification

Local fmt-check, both lint passes, full unit coverage, and build passed.
Updated integration-tag lint passed. GitHub run 35149935335 passed both
fmt/lint/unit coverage and Docker integration jobs.

## Resolution

Both CI jobs pass with all existing checks enabled, accessible pinned images,
and the runtime integration test aligned with the snapshot authority contract.

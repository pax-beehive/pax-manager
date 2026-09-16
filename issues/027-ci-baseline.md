# [ISSUE-027] CI fails formatting, lint, and object storage startup

**Status:** in-progress
**Severity:** high
**Component:** deployment
**Found:** 2026-09-16
**Resolved:** -

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

## Verification

Pending local checks and GitHub integration CI.

# [ISSUE-016] Public Node API exposure needs a reviewed launch boundary

**Status:** partial
**Severity:** blocker
**Component:** deployment / auth
**Found:** 2026-09-09
**Resolved:** -

## Summary

KEV-35 requires a complete route allowlist and non-interactive customer Node
access while preserving browser/admin boundaries. The existing launch tasks
assume Cloud Run; production was confirmed to use a home Ubuntu host and
Cloudflare Tunnel.

## Current note

The current v1 machine API now has a 28-route manifest, a method/template gate,
route parity and missing/invalid credential regression tests, a generated route
matrix, and a credential-free ingress probe. New unreviewed machine handlers
fail closed. Existing endpoint authentication is preserved.

This is a partial implementation: no live edge policy was changed or verified.
SSH inspection of `home_dev` confirmed loopback-only application bindings,
production authentication enabled, and a private Docker-network tunnel to
Manager on `api.lakeward.net` and `wsapi.lakeward.net`. `ws.lakeward.net` is the
Console, not Manager. Origin health returns 200 and protected origin endpoints
return 401. Both public Manager hostnames redirect machine requests to Access
login (302). A four-application Access change plan is prepared, contingent on
deploying and verifying the method/template guard. The correct dashboard
account was previously confirmed, but browser control is currently timing out.

On September 10, paxd CF service-token removal was committed locally as
`1fa5392`; its full test suite and CLI builds passed. The Manager guard passed
full tests and Linux compilation against an isolated copy of the deployed
`e0814a28eb65` baseline. Changed-file formatting and integration lint passed;
repository-wide checks still report existing formatting and nine lint findings.
The legacy `/api/agent/*` API remains registered and is not included in the
current Node exposure policy; its existing local issues are not resolved here.

## Remaining KEV-35 acceptance

- Apply exact machine bypass rules and retain user/admin protection (KEV-35).
- Run a real customer paxd registration, connection, and publication flow.

KEV-36 through KEV-40 track additional launch hardening separately.

See `docs/node_public_rollout.md` for code evidence, commands, and policy scope.

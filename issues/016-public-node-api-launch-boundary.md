# [ISSUE-016] Public Node API exposure needs a reviewed launch boundary

**Status:** resolved
**Severity:** blocker
**Component:** deployment / auth
**Found:** 2026-09-09
**Resolved:** 2026-09-10

## Summary

KEV-35 requires a complete route allowlist and non-interactive customer Node
access while preserving browser/admin boundaries. The existing launch tasks
assume Cloud Run; production was confirmed to use a home Ubuntu host and
Cloudflare Tunnel.

## Resolution

Resolved on 2026-09-10 for the current v1 node API and ACP tunnel.

- The 28-route method/template guard is deployed and unknown node routes fail
  closed. No database schema or user-auth defaults changed.
- The four reviewed machine path scopes are bypassed by a dedicated Access
  app. User/admin/OpenAPI and Console routes retain their existing login.
- paxd no longer requires, resolves or sends CF service-token credentials.
  The Ubuntu daemon runs `0.1.36+kev35`, based on its verified 0.1.36 source,
  with the existing remote configured as auth `none` and connected.
- Real fresh paxd registration, browser approval, Node Key control connection,
  ACP 101 upgrades, missing/invalid key rejection and user login were verified.
- Both public hostnames pass all 31 edge-aware rejection probes. The isolated
  test node, agent, local secret, daemon and Docker test volumes were cleaned up.
- Manager received the new daemon version heartbeat. The existing hosted agent
  is running, and the tunnel showed zero recent origin connection errors.

Detailed rollout, test limitations, exact policy IDs and rollback records are
in `docs/handoff_20260910_081500.md` and the remote deployment runbook.
Original production backups and legacy secret files remain for rollback.

KEV-36 through KEV-40 track additional launch hardening separately. The legacy
`/api/agent/*` API remains outside this exposure policy; its legacy issues are
not resolved by this change. Global Manager formatting/lint findings predate
this change. Source changes are committed locally, not published as a release.

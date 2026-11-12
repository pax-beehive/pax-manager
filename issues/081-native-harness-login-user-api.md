# [ISSUE-081] Native harness login has no owner-scoped user API

**Status:** resolved
**Severity:** medium
**Component:** api / auth / node control
**Found:** 2026-11-11
**Resolved:** 2026-11-11

## Summary

The current node API exposed harness upgrades but could not complete the native
Claude or Codex login already implemented by paxd.

## Round-trip affected

- [x] User login / API key
- [ ] Agent registration / connection
- [x] User-agent messaging round-trip

## Resolution

Added owner-scoped GET harness auth status and POST start/submit/cancel routes
through the authenticated node control WebSocket. All five existing paxd login
methods are supported. The API uses ephemeral commands and never writes login
codes, API keys, access tokens, or authorization URLs to Manager persistence.
Responses are uncached, error text is bounded and sanitized, and attempt
ownership is kept as short-lived nonsecret process memory. A pending-start
reservation allows status recovery when a start ACK is lost.

This fixes the current node user API. It does not add legacy agent login routes,
change provider token refresh, restart existing agent processes, or establish a
distributed control tunnel. Console integration is implemented separately.

## Operational limitation

Requests must reach the Manager instance holding the node control tunnel and
login ownership. Manager restart loses that metadata. Unknown daemon attempts
fail closed; wait for the original five-minute deadline before starting again.

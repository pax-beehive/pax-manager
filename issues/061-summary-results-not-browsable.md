# [ISSUE-061] Secretary results cannot be browsed by ordinary owners

**Status:** resolved
**Severity:** medium
**Component:** userapi / storage / Console
**Found:** 2026-11-02
**Resolved:** 2026-11-02

## Summary

The Secretary settings page reports progress but does not expose the resulting
summary content to ordinary users.

## Current behavior

The existing PostgreSQL summary browser requires administrator access. Ordinary
owners can read an exact summary reference through `search/read`, but cannot
discover their current summaries without a search query or an administrator UI.

## Resolution

Add `GET /api/v1/user/:user_id/secretary/summaries` for the authenticated owner.
Reuse immutable summary storage and the existing source visibility checks.
Return current revisions newest first with bounded owner-bound cursor pages.
The route has an eight-second deadline and no Elasticsearch dependency.
Read full bodies, claims and pinned evidence through the existing owner-scoped
`POST /api/v1/user/:user_id/search/read` route. The developer-only routes retain
their administrator gate and existing cursor behavior.

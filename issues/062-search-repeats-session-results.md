# [ISSUE-062] Search repeats a session as title, summary and message results

**Status:** resolved
**Severity:** medium
**Component:** search / userapi / storage / Console
**Found:** 2026-11-02
**Resolved:** 2026-11-02

## Summary

Document-level search makes one source session appear as several independent
cards when its title, summary and messages all match.

## Resolution

Add opt-in session grouping to the existing search endpoint. ES collapse uses
an owner/agent/session key, returns bounded best-document previews, and pages
groups using a separate shallow PIT/offset cursor. PostgreSQL revalidates every
returned match and supplies the canonical current session name. Secretary
summaries join only their explicit primary source, never an arbitrary coverage
message; standalone cross-session summaries stay separate. Exact group-key raw
search expands further matches without changing the Secretary raw-search path.

Existing projections need the explicit bounded migration documented in
`docs/search_session_groups.md`. Grouped search reports migration-required 503
until legacy documents are reprojected; raw search continues working.

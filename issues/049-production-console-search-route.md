# ISSUE-049 Production Console has no workspace search page

**Status:** open
**Severity:** medium
**Component:** deployment / Console
**Found:** 2026-10-31
**Resolved:** -

## Problem

A signed-in browser on paxworkspace.net returned 404 for /search. The existing
header search field did not expose the workspace search interface. The inspected
Console source contains src/app/search/page.tsx, but a backend-only US Manager
deployment does not publish that frontend route.

US backend search was separately enabled in ISSUE-048: the search route exists,
unauthenticated access returns 401, and PG-to-ES indexing is progressing.
Do not infer frontend availability from that backend deployment.

## Next step

Identify the registered Console build containing workspace search, its active
production frontend target and its existing deployment workflow. Deploy that
verified frontend version through normal Release, then verify signed-in keyword
search and source expansion against the current user's indexed data. Do not
mint user credentials or inject requests into a browser to bypass the missing UI.

This backend deployment change does not modify or deploy Console.

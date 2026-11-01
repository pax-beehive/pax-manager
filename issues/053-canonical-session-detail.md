# ISSUE-053 Opening an unlisted session requires scanning session pages

**Status:** resolved
**Severity:** medium
**Component:** userapi / session metadata
**Found:** 2026-11-01
**Resolved:** 2026-11-01

## Problem

A session deep link can contain only its canonical session ID. Existing detail
routes require node and agent IDs, while the user-session list is paginated.
Resolving missing routing context through that list can require many requests
and delay opening an older session.

## Resolution

Add GET /api/v1/user/:user_id/sessions/:session_id, with self supported, returning
the existing AgentSession data envelope. It uses the existing permission-scoped
single-session lookup and the same owner/team visibility rules as other session
reads. A missing legacy node ID is resolved through the authorized agent. The
existing bounded history-head metadata read supplies synchronization fields.
There is no session-list pagination, history hydration, Elasticsearch dependency
or new storage interface/schema.

BDD unit tests cover principal failure, required ID, absent/inaccessible data,
legacy node fallback and head-read errors. HTTP tests cover the generated route,
owner routing IDs, missing identity, unrelated ownership and absent sessions.
Console integration and production rollout are tracked separately. See
[the handoff](../docs/handoff_20261101_104500.md).

# ISSUE-047 Legacy journal conversions still scan history during startup

**Status:** resolved
**Severity:** high
**Component:** storage / startup
**Found:** 2026-10-31
**Resolved:** 2026-10-31

## Problem

The earlier startup fix removed two transport queue watermark aggregations but
left four unconditional legacy journal conversion UPDATEs in db/init.sql. Even
when no rows need conversion, PostgreSQL can scan the entire journal heap before
Manager starts listening.

On US, the direction and legacy error UPDATE plans used sequential scans of a
2,681 MB heap. A read-only equivalent of the error predicate exceeded a five-second
diagnostic statement timeout. The failed release was canceled after 60 seconds
while applying init.sql; the restored Manager took about 82 seconds to listen.
These observations do not attribute the entire startup duration to one statement.

## Resolution

Remove the remaining journal backfills for queue_id, direction, error_message and
legacy stream names from schema initialization. Keep schema definitions,
constraints, indexes, current journal writes and bounded background retention.
The change applies to the shared journal used by current node and legacy agent
traffic. Very old databases needing data conversion require an explicit migration.

Regression coverage applies the real schema twice to an isolated PG database,
preserves journal rows and queue watermarks exactly, and installs a statement-level
trigger that rejects even zero-row journal DML. The ordinary schema test also
rejects reintroducing startup journal UPDATE statements.

This issue is resolved in source. No production configuration, database contents,
container lifecycle or release state was modified during diagnosis or this fix.

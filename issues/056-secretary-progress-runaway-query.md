# Secretary progress reads can exhaust database I/O

- Status: resolved
- Severity: high
- Component: storage / Secretary
- Resolved: 2026-11-02

## Problem

Opening Secretary settings starts a coverage query across the owner's current
messages. The query fetched entire message and part JSON, joined messages twice,
and sorted the wide result before Go discarded noncanonical message types.
Concurrent reads could spill to disk and continue long after the user left.

On 2026-11-02, seven production progress queries were still active; the oldest
had run for more than 19 minutes. The worker continued publishing other summaries.
Queue processing and the progress request were separate operations.

## Resolution

Filter message roles/types before loading parts, aggregate ordered canonical
text and its SHA256 inside PostgreSQL, and return compact message metadata plus
hashes. Reuse the domain visibility policy, retain exact part validation and
Unicode whitespace semantics, and remove the duplicate message join and wide
global sort. Apply an eight-second context and transaction statement timeout.

The response, permissions, source-version comparison, and pagination contract
are unchanged. No schema migration, new index, or checkpoint rewrite is needed.
Console must also preserve its request timeout when a caller supplies a signal.

## Verification

Regression tests cover current and changed text, late/repartitioned parts,
hidden envelopes, ownership changes, Unicode whitespace, and SQL/Go hash parity.
The compact query completed a production read-only EXPLAIN ANALYZE in 7,023 ms
with cold reads and 4,018 ms on a repeat run, without a wide result sort;
no message contents were returned.

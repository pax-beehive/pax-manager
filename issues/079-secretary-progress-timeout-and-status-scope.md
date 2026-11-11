# ISSUE-079 Secretary progress times out and conversation failures obscure overall work

**Status:** partial
**Severity:** high
**Component:** Secretary / progress SQL / status presentation
**Found:** 2026-11-11
**Resolved:** -

## Problem

The progress query still checks each source message against display replacement
rows and repeatedly parses their replacement arrays. It was not covered by the
batched search-source access correction in issue 077. The actual owner-scoped
production SELECT exceeded its eight-second statement timeout. A one-second
lock timeout did not fire. No source content or credentials were exported.

Separately, the Manager mapped an unknown/invalid conversation to an overall
error before considering active processing. Console then preferred that
conversation's execution label over the overall state and described retries as
globally paused. Its Queued count also included blocked conversations. This can
make one old failed task look like a stopped Secretary while other work continues.

## Required behavior

- Read persisted progress counters instead of hashing all historical messages
  on each poll; retain current owner, deletion, archival and internal checks.
- Preserve exact global counts, current content hashes and pagination behavior.
- Keep active processing visible alongside conversation-specific diagnostics.
- Keep finite retry budgets and explicit controls for uncertain executions.
- Do not infer completion or failure from runtime idle status alone.

## Current note

The first correction materialized owner/session scopes, display classification
and replacement targets. The existing display partial index remains in use.
The source metadata filter, parts/hash processing and Go aggregation are unchanged;
the separate pending-source query was unchanged. That correction alone still
scaled with visible history and did not remove full-history work from polling.

The follow-up adds a persistent, body-free progress projection and an independent
bounded worker. Clean GETs read readiness, session counters and current scope
metadata without touching messages or parts. Message/part/display/access changes
reuse the transactional search change ledger even when ES and Secretary are off;
published coverage checkpoints and internal-session registrations also enqueue
changes. Index acknowledgements do not. Reverse indexes support old-owner and
deleted-message cleanup without scanning the projection or queue.

Historical discovery persists primary-key cursors and bounds raw pages before
owner filtering. New history queue entries commit before their source bodies
are read in a fresh transaction. Writers serialize projection changes and hold
committed dirty rows through generation acknowledgement. A deletion or ownership
change therefore preserves the cleanup obligation even before the first cached
message exists. Real PostgreSQL tests reproduce the former first-projection
deletion/owner-move race and verify the corrected fence.

The worker handles at most 128 messages per step, prioritizes live changes and
uses remaining capacity for historical staging. It waits between steps, backs
off errors exponentially and resumes after restart independently of models or
Secretary settings. Existing inactive owners begin bootstrap on their first
progress read; deployment does not scan all user accounts. Bounded request-side
preparation handles up to 256 messages; an unfinished projection or busy writer
returns an explicit updating 503 instead of partial zero counts. Console hides
stale counts/rows and continues its normal polling. Serialization/deadlock races
also return updating rather than an unexpected server error.

A 128-source/24-display regression reduced JSON array expansion loops from 3096
to 48. Sixty-three replacement-shape comparisons match the original full progress
page, with additional owner/session isolation checks. These measurements describe
the first SQL optimization, not the final cached read path.

Production read-only checks found zero differences across 15,787 old/new SQL
result rows in one snapshot. A separate same-snapshot plan pair returned 15,790
rows in 11,688.494 ms before and 2285.104 ms after. These are SQL-only timings,
not complete API timings; the second query may benefit from warmed buffers.
Display scans fell from 16,949 to one and array expansion loops from 86,562 to
538. The eight-second API limit remains unchanged.

Manager now preserves Processing alongside unknown/ordinary blocked diagnostics,
while retaining disabled, unavailable-agent, recovery and connection-backoff
precedence. The modified status function has 100% coverage. The companion Console
PR labels the backlog Unfinished, scopes warnings to affected conversations and
links to existing task controls.

Deployment and complete endpoint verification remain pending. This correction
does not reset old invalid-output budgets or restart unknown executions. An old
unknown attempt without a prepared request association or terminal proof still
requires an explicit owner-controlled restart with its existing fencing rules.

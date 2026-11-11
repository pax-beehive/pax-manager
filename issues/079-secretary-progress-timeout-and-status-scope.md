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

- Classify relevant display replacements once per progress query while keeping
  current owner, deletion, archival and internal-session checks.
- Preserve exact global counts, current content hashes and pagination behavior.
- Keep active processing visible alongside conversation-specific diagnostics.
- Keep finite retry budgets and explicit controls for uncertain executions.
- Do not infer completion or failure from runtime idle status alone.

## Current note

The progress query now materializes owner/session scopes, display classification
and replacement targets. The existing display partial index remains in use.
The source metadata filter, parts/hash processing and Go aggregation are unchanged;
the separate pending-source query is unchanged. No schema or index is added.

A 128-source/24-display regression reduced JSON array expansion loops from 3096
to 48. Sixty-three replacement-shape comparisons match the original full progress
page, with additional owner/session isolation checks. The new query builder has
100% coverage and the progress read method has 95.5% coverage.

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

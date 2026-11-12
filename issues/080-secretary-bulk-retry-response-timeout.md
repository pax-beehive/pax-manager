# ISSUE-080 Bulk Secretary retry times out after committing work

**Status:** partial
**Severity:** high
**Component:** Secretary / bulk retry / request acknowledgement
**Found:** 2026-11-11
**Resolved:** -

## Problem

The run endpoint synchronously reads full status, reconciles all owner history,
resets retry state and reads status again before acknowledging. Console waits
12 seconds for response headers. A committed retry can therefore appear failed
when the trailing status query times out. Repeating the action has no request
identity and can repeat reconciliation and epoch changes.

Read-only production diagnostics confirmed that the 14:35 local request updated
the owner's 155 jobs and cleared the four displayed retry failures. PostgreSQL
subsequently recorded canceled pending-count queries. A fresh execution for one
of those sources started at 14:42. The displayed invalid results were historical,
not evidence of a new failed attempt. No tasks were retried during diagnostics.

## Required behavior

- Persist an owner-scoped idempotent command and return an immutable 202 receipt.
- Never calculate status, read bodies or reconcile history in the request.
- Advance eligible existing job metadata in bounded, restartable transactions.
- Preserve completed, paused, leased and unresolved executions, including old
  unknown executions whose epoch was advanced by an earlier bulk retry.
- Never dirty all history or reset message coverage as a side effect of retry.
- Keep acknowledgement independent of subsequent status-refresh failures.

## Current note

The implementation adds a durable command queue and optional request_id to the
existing run endpoint. An independent maintenance worker applies bounded job
updates, preserving configuration/ownership fences and original coverage.
Same-owner retries with the same ID return the original receipt without new
effects. Legacy missing-ID requests remain supported but lack client-side
deduplication. Console retains an unconfirmed ID across retries/reload and
recognizes both new receipts and legacy successful status responses.

Deploy Console first, then Manager. Only the new Manager guarantees server-side
deduplication; the old Manager ignores request IDs. Existing unknown executions
still use explicit per-task recovery, as tracked in issue 074. Deployment and
verification of the actual endpoint remain pending.

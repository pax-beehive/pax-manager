# ISSUE-071 Secretary source-completion waits appear queued

**Status:** resolved
**Severity:** medium
**Component:** Secretary / storage / task status
**Found:** 2026-11-05
**Resolved:** 2026-11-05

## Problem

Prepare releases a job whose source messages lack reliable completion evidence
and checks it again after 30 seconds, without creating a batch or model attempt.
It did not persist the reason. The existing task API therefore presented the
source as queued or waiting for a retry, concealing why summaries made no progress.

## Resolution

Memory and PostgreSQL now persist the existing source-incomplete reason when
Prepare defers the source. The task API returns `waiting_source` and the safe
`source_incomplete` explanation, without advertising an automatic model retry.
After eligible source input becomes available, successful preparation clears only this
specific reason. Running and completed work do not retain the stale explanation.
PostgreSQL writes and clears the reason in the lifecycle transaction.

Waiting does not manufacture historical terminal events, restart unknown
executions, change checkpoints or consume an execution attempt. Issue 073 adds a
separate settled-text eligibility path for old idle sources without terminal
events; the wait reason still applies to active, streaming, or recently changed
input.

## Validation

Memory and isolated PostgreSQL lifecycle contracts cover repeated waits, task
metadata after reopening the store, unchanged checkpoints and attempt budgets,
reliable completion followed by preparation/execution/publication, preservation
of unrelated failures and fenced work, and PostgreSQL rollback on a failed clear.

The code requires normal Manager deployment. Tests used isolated data;
production completion markers were not changed.

## Affected code

- `internal/manager/storage/secretary_lifecycle_memory.go`
- `internal/manager/storage/secretary_lifecycle_postgres.go`
- `internal/manager/storage/secretary_source_wait.go`

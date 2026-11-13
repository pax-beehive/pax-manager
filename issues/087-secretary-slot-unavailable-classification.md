# ISSUE-087 Secretary session creation failure becomes unknown

**Status:** partial
**Severity:** high
**Component:** Secretary / ACP dispatch / connection retry
**Found:** 2026-11-13
**Resolved:** -

## Problem

An existing task was stuck as unknown after a structured slot_unavailable
response to session/new. Paxd returns this response before selecting/sending to
a native slot; Manager has not sent session/prompt. Treating the failure as an
uncertain prompt execution prevents the existing bounded connection retry.

## Current note

Only a structured retryable slot_unavailable error at session creation is
classified as not dispatched. Existing exponential connection backoff, jitter
and retry limits apply. An error message alone, an explicit unknown error,
timeouts, cancellation and post-prompt failures retain their previous semantics.

A real ACP regression verifies no prepared prompt record, waiting_retry,
connection count/deadline and no immediate claim. A dispatched prompt cancellation
remains uncertain. Historical unknown rows are not automatically rewritten.

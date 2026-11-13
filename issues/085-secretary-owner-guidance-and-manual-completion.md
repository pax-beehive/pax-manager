# ISSUE-085 Secretary owner guidance and manual completion

**Status:** partial
**Severity:** high
**Component:** Secretary / user controls / summary publication
**Found:** 2026-11-12
**Resolved:** -

## Problem

Repeated invalid output or a long repetitive session currently leaves the owner
with retry controls but no way to give persistent or one-operation instructions,
complete a chosen range manually, or correct an existing summary. Processing
progress must not be inferred from representative summary provenance.

## Current note

Owner default and session guidance have independent CAS revisions. Prepared
operations freeze applicable instructions, exact complete-message targets and
baseline revisions. Guided operations reuse existing execution batches. Human
completion atomically publishes a summary, acknowledges selected exact changes
and fences older workers. Human edits inherit sources and add no progress.
Source holes remain pending. Historical authorship is explicitly unknown.

The accompanying Console adds editors, selection, status and explicit active-work
replacement. Preparation caps at 4096 messages and provenance at 512; advanced
message-ID selection supports choosing a bounded subset of larger sources.
Deployment and real long-session outcome verification remain pending. Cross-
session relations and board maintenance are outside this change.

PostgreSQL conservatively resets a manual invalidation acknowledgement when
visible message parts are structurally rewritten, even if their concatenated
canonical text is unchanged. Such reshaping may request another stale-node
repair; canonical-equivalent acknowledgement reuse is deferred. Metadata-only
reconciliation and settings changes do not rewrite message parts.

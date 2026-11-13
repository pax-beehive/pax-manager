# ISSUE-086 Secretary model-copied evidence blocks useful summaries

**Status:** partial
**Severity:** high
**Component:** Secretary / generation contract / provenance
**Found:** 2026-11-13
**Resolved:** -

## Problem

Three failed attempts for a release session differed from their supplied source
only by U+202F becoming an ASCII space in a model-copied quote. The batch was
about 39 KiB, below the input limit. Exact quotation proves that a phrase exists;
it does not prove that a summary's conclusion follows from it. Requiring the
model to copy and repeat structured evidence introduced a failure gate without
semantic verification.

## Accepted design

New Secretary jobs use a frozen summary-sources-v2 output policy. The model
returns title, body and selected source message IDs, plus existing processing
outcomes. Optional additional original-source reads retain the existing exact
reference and verified-receipt contract. Model-copied claims and quotations are
not requested or persisted for this policy. Compatibility echoes of old claim
metadata are ignored rather than becoming a quotation-validation gate.

Manager still verifies ownership, exact current source versions, completed
source visibility, baseline revisions and selected source membership. It freezes
the original message references/snapshots and commits provenance with summary
versions and processing outcomes atomically. These checks provide traceability,
not proof of semantic correctness. No fuzzy quotation alignment or additional
model verifier is introduced.

Historical summaries remain unchanged. Already frozen empty/legacy-policy jobs
retain their earlier parsing and evidence contract. A deliberate new retry gets
the new policy; deployment alone does not reset historical work or checkpoints.

## Current note

Implementation, full unit tests/build/format/lint and isolated actual
HTTP/ACP/PostgreSQL acceptance passed (2061 checks, zero skips). New output-policy
and decoder helpers have 100% focused unit coverage. Contracts verify exact
snapshots, ownership isolation, atomic progress and unchanged historical claims.
Production deployment and historical-task acceptance remain explicit follow-ups.

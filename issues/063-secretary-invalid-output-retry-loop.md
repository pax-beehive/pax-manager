# Secretary replays rejected completed output indefinitely

- Status: resolved
- Severity: high
- Component: Secretary / generated-output validation / job scheduling
- Resolved: 2026-11-02

## Problem

A completed internal execution is recovered using a deterministic run ID. When
its generated summary fails schema, quote, or source-proof validation, the job
backoff retries the same stored response. Failure did not advance retry_generation,
so a healthy selected agent could never correct the rejected response.

A production metadata-only audit found 27 errored sources: 22 unsupported evidence
failures, two invalid read receipts, two oversized summary states, and one invalid
summary body. No model execution had been created for more than two hours despite
repeated attempts. Sampled evidence failures mostly contained an unselected
message reference or a quote absent from supplied text. The sample did not prove
stale baseline quotes or independent read receipts caused the live failures.
A single explicit generation retry subsequently published successfully.

## Resolution

Trusted generated-output validation boundaries now return a typed schema or
evidence failure. Only those failures, plus an exact typed HTTP 400 invalid-read-
receipt result, may advance the matching leased job to a fresh generation.
The budget allows two automatic fresh generations for unchanged canonical input,
summary baseline, settings, and input policy. The key and count persist in PG.
New input starts a separate budget; explicit retry and successful publication
reset it. Existing backoff remains in effect. Exhausted jobs retain their error
and replay the final cached attempt without further automatic model calls.

A sanitized persisted failure code gives the next prompt corrective guidance.
Changed or removed baseline source quotes are omitted from prompt evidence.
Read receipts for independently read pages/views of a selected related source
are reconstructed and verified against the same selected exact source version.
Ambiguous versions, invalid owners, forged signatures, wrong views, and unseen
quotes remain rejected.

Incomplete turns, approval, empty visible output, input/baseline, storage,
transport, permission, and stale-lease failures do not spend fresh generations.
No global run-policy bump, source backfill, or production model call is part of
the code change. The existing manual retry API remains available after budget
exhaustion.

## Verification

TDD covers bounded run identities, cached failures, durable budgets, changed
input, explicit retry, successful publication, stale leases, and disabled
settings. Real signed receipt tests cover recovery across pages and views and
all permission/version/quote denials. Isolated PG and actual local ACP acceptance
exercise persistence, scheduling, and immutable summary publication.

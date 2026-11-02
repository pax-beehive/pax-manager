# Rolling summary inputs expose fields rejected in generated output

- Status: resolved
- Severity: high
- Component: Secretary / summary validation
- Resolved: 2026-11-02

## Problem

The rolling prompt embeds persisted SummaryClaim objects, including internal
claim IDs and evidence shaped as message/version refs, quote, and view. The
instructions allow unchanged evidence to be retained exactly, but the generated
output decoder only accepts claims without IDs and evidence containing a
message_id and quote. Copying a valid prior claim therefore fails strict JSON
decoding before evidence can be checked.

On 2026-11-02, structural inspection of two production replies confirmed this
input/output mismatch. Seventy pending sources had the schema error; the
executor kept reading their completed turns and rejecting the same response.
These retries did not dispatch a new model call, but could not advance coverage.

## Resolution

Baseline claims are projected into the generated output wire shape, retaining
the authoritative persisted baseline for validation and size limits. Support
only the previously exposed typed claim/evidence fields in completed replies.
Evidence is bound to trusted source versions; conflicting or invented metadata
is rejected. RunID, retry generation, permissions, and quote checks are unchanged.

No production task mutation or new model invocation is part of this fix.

The completed-turn recovery regression fails if a new prompt/session is
dispatched, and verifies that the same RunID produces a valid publication from
the stored response. Negative tests retain version, receipt, quote, view, and
unknown-field validation. Genuine evidence failures are not automatically waived.

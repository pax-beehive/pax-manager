# ISSUE-075 Secretary rejects shortened baseline evidence quotes

**Status:** partial
**Severity:** high
**Component:** Secretary / evidence binding
**Found:** 2026-11-10
**Resolved:** -

## Problem

An unchanged summary's supplied evidence quote can be shortened to a literal
contiguous substring without changing its message version or introducing unread
text. The retained-evidence resolver nevertheless requires the entire old quote
to equal the new quote. It rejects the valid shorter excerpt with the same
generic error used for an unsupported quotation.

An owner-scoped, read-only production comparison examined eight quote-invalid
attempts from four source sessions. Six attempts contain this exact substring
case; five have no other unmatched quote in that comparison. The attempts have
no additional original-message tool reads. This is a comparison of persisted
input, output and read metadata inside PostgreSQL, not a complete publication
replay. Other publication checks, including the batch budget, may still reject
an output after quote binding succeeds.

The previous change added safe retry feedback and compact reference storage,
but did not change this resolver. Those improvements do not fix the false
rejection.

## Required behavior

- Accept a nonempty literal substring of evidence supplied by the same
  unchanged baseline node for the same source message.
- Return the shorter quote while retaining the exact original message-version
  reference and view. Do not mutate the old summary revision.
- Continue rejecting paraphrases, joined excerpts, evidence from another node,
  missing coverage and conflicting source/version/view/read metadata.
- Changed or removed messages and reset baselines must be verified again.
- Do not auto-requeue production tasks or relax exact quotation verification.

## Current note

The resolver accepts a shorter contiguous literal excerpt and retains the old
exact source version and view. Synthetic TDD reproduced the false rejection
before the fix; the corrected function has 100% unit coverage. Memory and real
PostgreSQL contracts verify publication/readback and unchanged historical
evidence. Full Go tests, build, format, lint, HTTP/ACP/PG acceptance and isolated
Docker integration passed.

The change is appended to Manager PR 227 and has not been deployed. Production
acceptance and any owner-authorized retry of old blocked tasks remain pending.
No raw production source, quote, read receipt or model output is included in
the repository.

# ISSUE-076 Secretary compares a read receipt as message identity

**Status:** open
**Severity:** medium
**Component:** Secretary / evidence read proof
**Found:** 2026-11-10
**Resolved:** -

## Problem

A synthetic replay found a separate contract inconsistency when generated
evidence uses an optional nested `message` reference and a valid top-level
`read_receipt`. The tuple, view, quote and verified read can all match, but
`bindRollingEvidence` compares the entire `MessageVersionRef` struct, including
its transient nested receipt, against the bound reference. Omitting the nested
receipt fails; duplicating the same valid receipt inside `message` succeeds.

The source-read validation layer permits the nested receipt to be absent when
the top-level proof is supplied. Message identity explicitly excludes receipts.
The final binder should compare exact version identity and validate supplied
proofs separately, while rejecting conflicting nested and top-level receipts.

## Boundary

This was not observed in the eight production quote-invalid attempts examined
for issue 075: none used nested message references or receipts. The baseline
substring correction does not change this comparison. Do not expand that fix
into unverified source guessing or removal of read-proof checks.

## Next steps

- Add a regression with a verified read, exact tuple and valid top-level proof,
  with and without a duplicate nested proof.
- Reject altered version tuples, mismatched views and conflicting proofs.
- Align the final binder with the source-read contract.

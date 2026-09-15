# [ISSUE-021] S3 presigned paths are encoded twice during signing

**Status:** resolved
**Severity:** high
**Component:** storage
**Found:** 2026-09-15
**Resolved:** 2026-09-15

## Summary

Attachment filenames containing spaces or other escaped characters produced
invalid S3 upload signatures. Presigned downloads used the same signer.

## Cause

The custom `headerBoundS3Presigner` replaced the S3 SDK default signer without
preserving `DisableURIPathEscaping`. An object path containing `%20` on the wire
was signed with `%2520` in the canonical URI. Plain ASCII filenames without
escaped characters hid the mismatch.

## Resolution

Set `DisableURIPathEscaping` alongside `DisableHeaderHoisting` in the shared
presigner. This fixes shared S3 presigned PUT and GET operations, including user
attachments. No frontend API or object naming change is needed.

## Verification

`TestS3PresignedObjectPathSignature` rebuilds each wire request and checks its
signature using S3 URI rules. PUT and GET cases cover plain names, single and
repeated spaces, plus signs, percent signs, literal percent escapes, query and
fragment characters, and Unicode. The plain-name cases passed before the fix;
the other 14 cases failed before the fix and pass afterward.

Verification uses local credentials and does not contact a deployed S3 bucket.
The full `go test -count=1 ./...` suite and the manager build also pass with
Go 1.26. The test suite requires permission to bind local HTTP listener ports.

# [ISSUE-038] Unicode artifact filenames break signed downloads

**Status:** resolved
**Severity:** high
**Component:** userapi / object storage
**Found:** 2026-10-13
**Resolved:** 2026-10-13

## Cause

The session artifact content endpoint constructed the signed
`response-content-disposition` override by placing the raw filename inside
`filename="..."`. S3 rejects non-Latin-1 header values with `InvalidArgument`.
Chinese filenames therefore failed after the browser followed the signed URL.
The same construction also removed literal quotes from filenames.

This construction dates to commit `731e244` (2026-07-09), before the S3 migration
in `ec443a0` (2026-08-18). Artifact publication and attachment content endpoints
already use MIME parameter encoding. This failure is separate from the Worker
`invalid_origin` response.

## Resolution

The user API endpoint `/api/v1/user/self/artifacts/:artifact_id/content/:content_ref`
now uses `mime.FormatMediaType`, matching the other download endpoints. Unicode
filenames use an ASCII-only UTF-8 extended parameter; quoted ASCII filenames are
escaped without losing characters. Both inline and attachment responses use the
same encoding before signing, for JSON tickets and browser redirects.

No node or legacy agent API contracts change. Existing objects need no rename or
re-upload. Rollout is pending; previously issued signed URLs retain their old
query values and must be requested again after deployment.

## BDD verification

Given ASCII, quoted ASCII, Chinese, and accented/emoji filenames, when requesting
inline or attachment content through a ticket or redirect, the signed header is
ASCII-only and decodes to the exact original filename. The regression suite
fails against the old implementation for all non-ASCII and quoted cases.

Validation passed with `GOWORK=off`: `make fmt-check lint`,
`go test -count=1 -coverprofile=/tmp/unicode-download-coverage.out ./...`, and
`go build -o /tmp/pax-manager-build-check ./cmd/manager`. The changed encoding
statement has 100% coverage; this is not a claim of 100% whole-handler coverage.
The full test suite required local socket access for its existing HTTP servers.

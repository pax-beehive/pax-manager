# ISSUE-011 User-controlled artifact bucket/object gets server-signed GCS URLs (confused deputy)

**Status:** resolved
**Severity:** high
**Component:** storage / api
**Found:** 2026-07-18
**Resolved:** 2026-07-18

## Summary

`CreateSessionArtifact` persists caller-supplied
`contents[].bucket/object/generation/storage_uri` verbatim, and the content
download endpoint signs a V4 GET URL for whatever bucket/object is stored,
using the manager's signing service account. Any authenticated user can
obtain signed download URLs for arbitrary GCS objects the service account can
read.

## Round-trip affected

- [ ] User login / API key
- [ ] Agent registration / connection
- [ ] User-agent messaging round-trip

## Current behavior

- `handleCreateSessionArtifact` (`internal/manager/artifact_handlers.go:163`)
  unmarshals the body and stores contents unchanged; the session ownership
  check at `:179` is skipped when `session_id` is empty.
- `createSessionArtifactTx`
  (`internal/manager/storage/postgres_artifacts.go:190`) inserts
  bucket/object/generation/storage_uri as provided (memory store mirrors).
- `handleGetArtifactContent` (`internal/manager/artifact_handlers.go:220`)
  reads the caller's own artifact row and calls
  `SignObjectDownloadURL(content.Bucket, content.Object, ...)`.
- `SignObjectDownloadURL` (`internal/manager/paxd_artifacts_gcp.go:46`) signs
  any bucket/object via `iamcredentials.SignBlob` with no allowlist against
  the configured session-artifact bucket or per-user object prefix.

Exploit: create an artifact whose content points at a deterministically named
object in any readable bucket (e.g. release buckets), then request its
content URL to receive a 15-minute signed GET URL.

## Expected behavior

The server only signs downloads for objects it placed itself: reject or strip
caller-supplied bucket/object/generation/storage_uri on direct artifact
creation (the upload flow assigns object names server-side), and/or validate
bucket and object prefix against the configured session-artifact bucket
before signing.

## Impact

Confused-deputy read access, as the signing service account, to any GCS
object with a known or guessable name in any bucket the account can read.
Per-user artifact isolation currently rests on object-name secrecy alone.

## Affected code

```
internal/manager/artifact_handlers.go:163-187 - create accepts contents verbatim
internal/manager/artifact_handlers.go:220-278 - signs stored bucket/object
internal/manager/storage/postgres_artifacts.go:190-206 - verbatim insert
internal/manager/storage/memory_artifacts.go - mirror
internal/manager/paxd_artifacts_gcp.go:46-92 - unrestricted signing
```

## Proposed fix

Two layers: (1) in create, ignore/reject GCS fields supplied on
`CreateSessionArtifact` contents (server-assigned only via the upload
completion flow); (2) in `handleGetArtifactContent`, require
`content.Bucket == <configured session artifact bucket>` before signing.

## Resolution

Two layers landed. `normalizeCreateSessionArtifactRequest` now rejects
contents that carry caller-supplied GCS backing fields (bucket, object,
generation, storage_uri) with 400; gcs-backed contents are only created by
the server-side upload completion flow. `handleGetArtifactContent` now also
requires the stored content bucket to equal the configured session-artifact
bucket before signing, returning 403 otherwise. Regression tests cover both
layers in `internal/manager/server_test.go`, and the existing
upload/complete/content-URL flow still passes.

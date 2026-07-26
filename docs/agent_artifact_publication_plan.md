# Agent Artifact Publication Plan

## Status

Planning only. This document does not authorize implementation or deployment.

## Summary

Add a fire-and-forget artifact publication path for agents. An agent provides a
local file path and an optional display title. The MCP call returns after paxd
has taken a durable local snapshot and persisted a background job. Hashing,
manager registration, deduplication, GCS upload, retry, conversation display,
preview, and download continue asynchronously without agent involvement.

The design separates two concepts:

- Artifact publication: one user-visible occurrence produced by an agent tool
  call.
- Session artifact: the immutable uploaded file that can be reused by multiple
  publications.

A publication has its own opaque ID and stable conversation URI. The final file
identity is controlled by pax-manager with:

```text
owner_user_id + session_id + normalized_filename + sha256
```

The publication ID is a correlation ID for one display occurrence. It is not an
idempotency key and does not determine file identity.

## Goals

- Give the agent one small MCP operation with only a path required.
- Return from MCP without waiting for hashing, GCS upload, or artifact
  finalization.
- Guarantee that an accepted publication no longer depends on the agent's
  source path.
- Keep upload state, retry state, GCS tickets, offsets, and checksums hidden
  from the agent.
- Make user-visible artifact cards durable and recoverable through the same
  history projection and reconciliation model used by conversation displays.
- Deduplicate identical files in pax-manager without deduplicating individual
  conversation display occurrences.
- Make finalized artifact content immutable and generation-pinned.
- Support safe inline preview and explicit download.

## Non-Goals For The First Version

- Publishing directories. The first version accepts regular files only.
- Letting agents update or delete finalized artifacts.
- Letting agents inspect upload progress or retry uploads.
- Automatically deleting attachment records, artifact records, failed jobs, or
  local artifact spools.
- Streaming a file to GCS before paxd has taken ownership of a stable local
  snapshot.
- Replacing the existing user-authenticated artifact APIs in the first slice.

## Core Invariants

1. MCP returns `accepted=true` only after paxd owns an immutable local snapshot
   and the local publish job transaction has committed.
2. After acceptance, deleting or modifying the original source path cannot
   change the published bytes.
3. A publication remains addressable by one stable publication ID while its
   internal state changes.
4. An artifact is identified in pax-manager by owner, session, normalized
   filename, and SHA-256.
5. Multiple publications may resolve to the same artifact.
6. A finalized artifact never receives another upload ticket.
7. Preview and download are pinned to the finalized GCS generation.
8. WebSocket events are notifications. Database state and durable conversation
   history are the source of truth.
9. Reconciliation is idempotent. Replaying ACP frames, publication reports, or
   completion requests does not create duplicate user-visible cards.

## Agent-Facing MCP Contract

Expose one tool:

```json
{
  "name": "publish_artifact",
  "inputSchema": {
    "type": "object",
    "required": ["path"],
    "properties": {
      "path": {
        "type": "string"
      },
      "title": {
        "type": "string"
      }
    }
  }
}
```

Example call:

```json
{
  "path": "/workspace/output/report.pdf",
  "title": "Analysis report"
}
```

Successful tool output:

```json
{
  "accepted": true,
  "pax_artifact_publication": {
    "publication_id": "apub_..."
  }
}
```

The output is deliberately small. The agent is not asked to retain the
publication ID or perform a follow-up call.

The only synchronous errors are failures to accept responsibility for the
file, such as:

- Missing, unreadable, or non-regular source path.
- Unsupported file size.
- Failure to create a stable local snapshot.
- Failure to commit the local publish job.

Manager unavailability and GCS unavailability after local acceptance are
background failures and must not turn a completed MCP call into an agent-owned
workflow.

## Local Acceptance In paxd

The MCP process delegates to the running paxd daemon. The MCP process must not
own the long-running upload.

The daemon performs:

1. Resolve and validate the source path.
2. Open the source as a regular file and reject unsupported file types.
3. Generate a cryptographically random publication ID.
4. Create a daemon-owned spool directory.
5. Create a stable snapshot:
   - Prefer filesystem reflink when available.
   - Fall back to a byte copy.
   - Do not use a hard link because later source writes would mutate the
     accepted bytes.
6. Persist the publish job and snapshot path in one durable local transaction.
7. Return the accepted publication ID to MCP.
8. Schedule background processing.

The snapshot may take local I/O time, but the MCP call does not wait for cloud
network I/O.

Suggested spool layout:

```text
<paxd-data-dir>/artifact-spool/<publication-id>/content
<paxd-data-dir>/artifact-spool/<publication-id>/metadata.json
```

The metadata sidecar is optional if all required state is in SQLite. It must
not be a second source of truth.

## paxd Local State

Add a local table owned by the paxd daemon store:

```sql
CREATE TABLE artifact_publish_jobs (
    publication_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    source_filename TEXT NOT NULL,
    display_title TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    spool_path TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    artifact_id TEXT NOT NULL DEFAULT '',
    upload_id TEXT NOT NULL DEFAULT '',
    resumable_url TEXT NOT NULL DEFAULT '',
    uploaded_bytes INTEGER NOT NULL DEFAULT 0,
    ticket_expires_at TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

Recommended local states:

```text
accepted
registering
hashing
preparing
uploading
completing
available
retry_wait
failed
```

These states are internal. They must not appear in the MCP input contract.

The resumable URL is a bearer credential. Store it only in the protected paxd
database, never log it, and redact it from diagnostics.

At daemon startup, reload unfinished jobs and resume them. A missing resumable
session may restart from byte zero without changing the publication ID.

## Manager Data Model

### New artifact_publications Table

Add a durable user-visible occurrence:

```sql
CREATE TABLE artifact_publications (
    publication_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued',
    artifact_id TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_artifact_publications_session_created
    ON artifact_publications(owner_user_id, session_id, created_at);
```

Recommended manager publication states:

```text
queued
hashing
uploading
available
failed
```

The publication is mutable workflow state. The finalized session artifact is
immutable content.

### Reuse artifact_uploads

Reuse the existing `artifact_uploads` table as the manager-side uniqueness and
upload coordination row. Add:

```text
artifact_id
node_id
agent_id
```

Add a unique index:

```sql
UNIQUE (
    owner_user_id,
    session_id,
    filename,
    sha256
)
```

Normalization rules must be deterministic:

- Use the basename only.
- Reject empty, dot, and dot-dot names.
- Preserve case in the first version.
- Normalize SHA-256 to lowercase hexadecimal.
- Do not include title, MIME type, path, or publication ID in file identity.

The title is presentation metadata. It does not create a distinct artifact.

### Reuse session_artifacts And session_artifact_contents

Keep finalized artifact metadata in `session_artifacts` and the immutable main
content in `session_artifact_contents`.

The existing `session_artifacts.message_id` field must not be the only display
binding. One artifact can appear in multiple message parts. Durable display
bindings remain in `message_parts.artifact_uri`.

No update API should modify finalized content rows.

## Node-Scoped Manager API

All endpoints use node authentication. pax-manager derives
`owner_user_id` from the authenticated node and validates that the supplied
agent and session belong to that node and owner. The agent never supplies owner,
bucket, object, generation, or upload state.

### Register A Publication

```http
PUT /api/v1/node/artifact-publications/:publication_id
```

Request:

```json
{
  "source": {
    "agent_id": "agent_1",
    "session_id": "session_1"
  },
  "filename": "report.pdf",
  "title": "Analysis report"
}
```

Response:

```json
{
  "publication": {
    "publication_id": "apub_...",
    "status": "queued"
  }
}
```

The operation is idempotent for the same authenticated node and source
identity. Reuse of the same publication ID with different ownership or source
fields returns conflict.

### Prepare Or Reuse An Artifact

```http
POST /api/v1/node/artifact-publications/:publication_id/prepare
```

Request:

```json
{
  "filename": "report.pdf",
  "content_type": "application/pdf",
  "size_bytes": 123456,
  "sha256": "..."
}
```

If an identical finalized artifact already exists:

```json
{
  "status": "available",
  "artifact_id": "art_..."
}
```

If upload is required:

```json
{
  "status": "upload_required",
  "artifact_id": "art_...",
  "upload": {
    "upload_id": "artup_...",
    "protocol": "gcs_resumable",
    "method": "POST",
    "url": "...",
    "headers": {
      "Content-Type": "application/pdf",
      "x-goog-resumable": "start"
    },
    "chunk_alignment": 262144,
    "expires_at": "..."
  }
}
```

Concurrent prepare calls for the same natural file identity must return the same
artifact/upload coordination row.

### Complete An Upload

```http
POST /api/v1/node/artifact-uploads/:upload_id/complete
```

Request:

```json
{}
```

pax-manager reads object attributes itself and verifies:

- Object existence.
- Declared size.
- Content type normalization.
- GCS generation.
- Available checksum metadata.

Completion atomically:

1. Creates or returns the immutable session artifact.
2. Stores the generation-pinned main content.
3. Marks the upload completed.
4. Links all waiting publications for the same upload identity.
5. Marks those publications available.

The endpoint is idempotent and always returns the same finalized artifact after
success.

### Report A Permanent Background Failure

```http
POST /api/v1/node/artifact-publications/:publication_id/failed
```

Request:

```json
{
  "error_code": "local_hash_failed",
  "message": "..."
}
```

Transient network and ticket errors must remain in paxd retry state and should
not be reported as permanent failures.

## Background Hashing And Upload

The background worker operates only on the paxd-owned snapshot.

1. Register the publication with pax-manager.
2. Compute SHA-256 and MIME type from the snapshot.
3. Call prepare.
4. If manager returns an existing artifact, link the publication and finish.
5. Otherwise initiate a GCS resumable session.
6. Upload aligned chunks, for example 8 MiB chunks aligned to 256 KiB.
7. On a network error, query the GCS resumable session for the committed offset
   and continue.
8. If the initiation ticket expires before a resumable session is established,
   request a fresh ticket.
9. If the resumable session is invalid after restart, start the upload again
   from byte zero.
10. Call the idempotent complete endpoint until its result is known.

The manager does not need per-chunk rows. GCS and paxd local job state own the
upload offset.

## Conversation Projection And Reconciliation

The artifact card must use the existing durable history model rather than
directly inserting a guessed current assistant message from the upload
completion handler.

### Raw Event Projection

ACP tool events continue to be projected as raw durable messages:

```text
tool_call
tool_call_update running
tool_call_update completed
```

The successful MCP result contains:

```json
{
  "pax_artifact_publication": {
    "publication_id": "apub_..."
  }
}
```

### Display Projection

When a terminal tool update and its registered publication are both present,
the reconciler upserts:

```text
message_type: pax:artifact
logical_key: artifact-publication:<session-id>:<tool-call-id>:display
parent_message_id: <terminal-tool-update-message-id>
```

Example raw display payload:

```json
{
  "publication_id": "apub_...",
  "replaces_message_ids": [
    "raw_tool_call_message",
    "running_tool_update_message",
    "terminal_tool_update_message"
  ]
}
```

The message part is:

```json
{
  "part_type": "artifact",
  "artifact_uri": "artifact-publication://apub_.../main"
}
```

The URI remains stable while the publication moves from queued to available.
The message part does not need to be rewritten when upload finishes.

Generalize the normal transcript display filter so `pax:artifact` participates
in the same `replaces_message_ids` contract as existing Pax invocation display
messages. Debug history continues to expose raw tool frames.

### Reconciliation Triggers

Run the same idempotent reconciler after:

- Projection of a terminal ACP tool update.
- Publication registration.
- Publication state changes.
- Manager startup or a periodic repair scan.

The two durable facts may arrive in either order. If only one is present, the
reconciler does nothing and a later trigger completes the projection.

The repair scan finds terminal tool updates containing a publication marker but
missing their deterministic display logical key. It does not depend on a
browser WebSocket connection.

## User-Facing Read, Preview, And Download

The conversation contains a publication URI, so the frontend always resolves
publication state first:

```http
GET /api/v1/user/:user_id/artifact-publications/:publication_id
```

Example queued response:

```json
{
  "publication": {
    "publication_id": "apub_...",
    "status": "uploading",
    "filename": "report.pdf",
    "title": "Analysis report"
  }
}
```

Example available response additionally includes artifact metadata.

Content route:

```http
GET /api/v1/user/:user_id/artifact-publications/:publication_id/content/main
```

Query:

```text
disposition=inline
disposition=attachment
redirect=true
```

When available, the route signs a short-lived URL pinned to the finalized GCS
generation. Before availability, the metadata endpoint reports current state
and the content endpoint returns a typed not-available response.

Initial preview policy:

- `image/*`: inline image.
- `application/pdf`: inline PDF viewer.
- `text/plain`, Markdown, and JSON: bounded fetch followed by safe rendering.
- HTML: sandboxed preview or download-only until a safe renderer is complete.
- Other binary types: metadata and download only.

Browser WebSocket publication updates are an optimization. Reconnect and
history reload must reconstruct the card from durable messages and resolve its
current state from the user API.

## Failure Semantics

### Before MCP Acceptance

Return an MCP error. No publication guarantee is made.

### After MCP Acceptance

The agent is no longer responsible.

- Manager unavailable: retry registration with backoff.
- Upload ticket expired: request a new ticket.
- Network interruption: query the resumable offset and continue.
- paxd restart: reload the local job and resume or restart.
- Complete response lost: repeat complete.
- Browser ready event lost: resolve publication state from the user API.
- Permanent local spool or hash error: report publication failed.
- Repeated remote integrity failure: retain the spool, mark the publication
  failed, and expose the failure to the user.

The first version performs no automatic destructive cleanup.

## BDD/TDD Delivery Slices

Each slice begins with black-box or package-level behavior tests and ends with a
working vertical behavior.

### Slice 1: paxd Durable Fire-And-Forget Acceptance

Scenarios:

- Given a regular file, when MCP publishes it, then the daemon snapshots it,
  commits a local job, and returns accepted without waiting for manager.
- Given manager is unavailable, when local acceptance succeeds, then MCP still
  returns accepted and the job remains queued.
- Given the source is modified or deleted after acceptance, then the spool
  bytes remain unchanged.
- Given snapshot or job persistence fails, then MCP returns an error.
- Given paxd restarts, then the accepted unfinished job is reloaded.

Deliverables:

- Local table and store methods.
- Daemon publish service.
- Local daemon command used by MCP.
- `publish_artifact` MCP schema and controlled result marker.

### Slice 2: Manager Publication Registration

Scenarios:

- Node-authenticated paxd can register a publication for its own agent/session.
- Cross-node, cross-owner, or invalid session registration is rejected.
- Repeating the same registration returns the same publication.
- Reusing a publication ID with different source identity returns conflict.
- User publication reads enforce conversation ownership.

Deliverables:

- `artifact_publications` schema and store.
- Node registration endpoint.
- User metadata endpoint.
- Generated API and OpenAPI updates where required.

### Slice 3: Natural File Identity And Deduplication

Scenarios:

- Two publications with the same owner, session, filename, and SHA-256 share one
  artifact upload.
- Same bytes with a different filename create a different artifact identity.
- Same filename and bytes in a different session do not reuse the session
  artifact.
- Concurrent prepare requests return one upload row.
- An already available artifact skips GCS upload.

Deliverables:

- Artifact upload uniqueness migration.
- Idempotent prepare endpoint.
- Publication-to-artifact linking.

### Slice 4: Conversation Artifact Display Reconciliation

Scenarios:

- Terminal tool update first, publication registration second, produces one
  display card.
- Publication registration first, terminal update second, produces one display
  card.
- Replaying either event does not duplicate the card.
- Normal transcript hides raw tool frames and shows the artifact card.
- Debug transcript retains raw frames.
- Two publish calls for the same artifact produce two display cards with one
  shared artifact.
- A repair scan reconstructs a missing display projection.

Deliverables:

- Publication marker parser.
- `pax:artifact` display projector.
- Generalized replacement display filtering.
- Repair reconciler.

### Slice 5: Resumable GCS Upload

Scenarios:

- Background worker uploads aligned chunks and completes the publication.
- Interrupted upload resumes from the GCS committed offset.
- Expired initiation ticket is refreshed.
- Invalid resumable session restarts from byte zero.
- Lost complete response is resolved by idempotent completion.
- paxd restart resumes an unfinished local job.

Deliverables:

- GCS resumable uploader in paxd.
- Cloud client methods for publication prepare and completion.
- Local progress persistence.
- Idempotent manager completion.

### Slice 6: Preview And Download

Scenarios:

- Queued publication returns metadata but no content URL.
- Available publication returns a generation-pinned inline URL.
- Download disposition produces an attachment URL.
- Unauthorized users cannot resolve publication metadata or content.
- Duplicate publications resolving to one artifact each remain readable.

Deliverables:

- Publication content endpoint.
- Preview metadata.
- Signed URL integration.
- Frontend artifact card and renderer integration in the console repository.

### Slice 7: End-To-End Recovery

Scenarios:

- Agent publishes, MCP returns accepted, source is deleted, and the user
  eventually sees a downloadable artifact.
- Browser WebSocket disconnects before upload completion and history reload
  still shows the correct final state.
- Manager restarts between terminal tool update and publication registration,
  and reconciliation repairs the display.
- paxd restarts during upload and resumes without involving the agent.
- Repeated publishing of an unchanged file reuses storage but creates a new
  user-visible publication.

Deliverables:

- Container integration scenario with fake or mock GCS.
- Cross-repository compatibility tests.
- Operational metrics and structured logs.

## Verification Plan

For pax-manager:

```bash
make fmt-check
make lint
GOCACHE=/tmp/pax-manager-go-cache go test -count=1 ./...
GOCACHE=/tmp/pax-manager-go-cache go build -o /tmp/pax-manager-build-check ./cmd/manager
```

For paxd:

```bash
gofmt -w <changed-go-files>
git diff --check
GOCACHE=/tmp/paxd-go-cache go test -count=1 ./...
GOCACHE=/tmp/paxd-go-cache go build -o /tmp/paxd-build-check ./cmd/paxd
```

For end-to-end behavior:

```bash
make integration-test
make integration-down
```

Add protocol contract tests that run the same JSON fixtures against pax-manager
and paxd request/response types.

## Observability

Structured logs and metrics should use publication ID, artifact ID, upload ID,
agent ID, and session ID, but never log signed URLs or local file contents.

Suggested metrics:

```text
artifact_publications_accepted_total
artifact_publications_available_total
artifact_publications_failed_total
artifact_publish_queue_depth
artifact_publish_snapshot_seconds
artifact_publish_hash_seconds
artifact_upload_bytes_total
artifact_upload_resume_total
artifact_upload_retry_total
artifact_publication_reconcile_total
artifact_publication_reconcile_repair_total
```

## Rollout And Compatibility

1. Deploy manager schema and node APIs before advertising the MCP tool.
2. Deploy paxd with the local queue and background worker.
3. Advertise `publish_artifact` only when the configured manager supports the
   publication API version.
4. Deploy console publication-card resolution and preview.
5. Keep existing user artifact upload routes operational during migration.

If older consoles encounter `pax:artifact`, they should retain the message part
as unknown structured content rather than dropping the entire message.

## Decisions Captured

- Agent publication is asynchronous and fire-and-forget.
- MCP success means durable local acceptance, not remote availability.
- paxd owns the background lifecycle after acceptance.
- Publication identity and immutable artifact identity are separate.
- File identity is manager-controlled by owner, session, filename, and SHA-256.
- User-visible placement uses durable ACP history reconciliation and display
  replacement, not upload callback timing.
- Publication URIs remain stable while internal state changes.
- The first version does not automatically clean up durable records or spools.

## Open Implementation Parameters

These parameters should be set before production rollout but do not change the
architecture:

- Maximum accepted file size.
- Default GCS chunk size.
- Retry backoff and the threshold for permanent failure.
- Exact safe-preview size limits.
- Whether source paths are restricted to session workspace roots.
- Whether successful local spools remain indefinitely or are cleaned only by a
  future explicit administrative operation.

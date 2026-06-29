# Team Memex Design

## Summary

Team memex is the internal domain for the first LLM Wiki feature. The first
version lets pax-manager maintain team-visible Markdown documents from team
agent message history by using a hosted LLM executor. Humans can read the
published wiki, but cannot edit it.

This is not the knowledge capsule flow. Knowledge capsules are user-directed
handoff objects between sessions. Team memex is an automatically maintained
team knowledge surface.

## Product Scope

The external product name is LLM Wiki. The internal package, table, and domain
language should use `team_memex`.

The first version should support:

- Team-scoped active Markdown documents.
- A system-generated `index.md`.
- Manual owner/operator-triggered maintenance runs.
- Hosted DeepSeek maintenance executor.
- Lexical document search for the hosted executor.
- Automatic publish after manifest validation.
- Human read-only APIs for active documents.
- Internal run logs with source session and message references.

## Non-Goals

The first version should not include:

- User-agent memex retrieval through paxl.
- A local agent identity broker.
- Agent-scoped memex grants.
- A user-agent websocket maintenance executor.
- Embeddings or vector search.
- Scheduler or cron-style automatic runs.
- Human editing.
- Draft, approval, or rollback workflows.
- Hard delete.
- Secret redaction or PII filtering.
- Source session or message references in human read APIs.
- Rationale text in human read APIs.

Future work may add paxl retrieval, user-agent websocket maintenance, scheduler
runs, backfill, richer memex graph features, trails, and Xanadu-style relations.

## Terms

- Team memex: The internal domain that stores and maintains team knowledge.
- LLM Wiki: The human-facing product surface for reading team memex documents.
- Document: A Markdown knowledge file with a stable path.
- Active document: A document visible in index, list, get, search, and run
  inputs.
- Archived document: A hidden document that still reserves its path.
- Maintenance run: One attempt to process new team-agent messages into memex
  document operations.
- Executor: The component that decides how to create, update, or archive memex
  documents.
- Hosted executor: The first executor implementation, backed by DeepSeek v4
  flash.
- Manifest: The executor output that manager validates and publishes.

## Access Model

Human read access should use team membership:

- Team members can read active memex documents.
- Owner/operator can trigger a manual maintenance run.
- Archived documents are not returned through normal read APIs.

The first version deliberately has no agent-facing read path. This avoids the
cross-team local-agent access problem until paxl, paxd, and local agent identity
are ready for a scoped grant model.

## Ingestion Policy

Maintenance input comes from agents that are attached to the team.

The first version uses future-only ingestion:

```text
team_agent.joined_at is the lower bound for source messages
historical messages before joined_at are not ingested
```

Backfill should be a future explicit mode, not an implicit side effect of
joining an agent to a team.

Cursor advancement is message-based, not session-based:

```text
executor returns processed_message_ids
processed_message_ids must be a continuous prefix or continuous range from the
run input
manager advances the cursor only through the last processed message
partial runs leave unprocessed messages for the next run
```

Session ids and message ids are stored in internal run logs but are not exposed
on human read APIs.

## Document Model

Documents are Markdown files with a stable path.

Recommended fields:

```text
team_memex_documents
- document_id
- team_id
- path
- title
- summary
- tags_json
- body_md
- status
- created_at
- updated_at
- archived_at
```

Recommended status values:

```text
active
archived
```

Path rules:

- `path` is document identity.
- Active and archived documents both reserve their path.
- `create_doc` must fail if the path ever existed.
- `update_doc` can update active documents only.
- `archive_doc` can archive active documents only.
- First version does not support restore.
- First version does not support hard delete.

## Index

`index.md` is system-generated and read-only for the executor.

The index should include active documents only. Archived documents should not
appear in the index, search results, run inputs, or normal human reads.

The index groups documents by path directory:

```markdown
# Team LLM Wiki

## engineering

- [Runtime State](engineering/runtime-state.md) - Runtime state conventions.
  Updated 2026-06-28.
- [Team Memex](engineering/team-memex.md) - Team-maintained operational memory.
  Updated 2026-06-28.

## product

- [LLM Wiki](product/llm-wiki.md) - Human read surface for team memex.
  Updated 2026-06-28.
```

Index rows should include:

```text
path
title
summary
updated_at
tags, if useful
```

Index rows should not include:

```text
body_md
session_id
message_id
source_agent_id
rationale
```

## Maintenance Run Flow

The first version should run synchronously enough for a manual API to return a
run record, but the service design should not require a handler to own all
business logic.

Recommended flow:

1. Owner/operator requests a manual run.
2. pax-manager creates a run record and takes a team-level run lock.
3. pax-manager selects team-agent messages after the current cursor.
4. pax-manager builds `index.md`, `new_messages.md`, `constraints.json`, and
   run instructions.
5. The hosted DeepSeek executor searches and reads active memex documents
   through workspace tools.
6. The executor returns a JSON manifest.
7. pax-manager validates the manifest.
8. If validation fails and the error is retryable, pax-manager sends the
   validation report back to the executor for repair.
9. When validation passes, pax-manager publishes all operations atomically.
10. pax-manager advances the message cursor to the last processed message.
11. pax-manager records final run status and attempt details.

Publishing should be all-or-nothing per accepted manifest. If any operation is
invalid, no operation from that attempt should publish.

## Hosted Executor Harness

The harness should be narrow. It is not a general agent harness. Its job is to
let DeepSeek maintain team memex documents under manager control.

Recommended package shape:

```text
internal/manager/memex
  models.go
  service.go
  store.go
  index.go
  search.go
  manifest.go
  validator.go

internal/manager/memex/llm
  executor.go
  deepseek.go
  prompt.go
```

The executor interface should not depend on DeepSeek:

```go
type MaintenanceExecutor interface {
    Run(ctx context.Context, input MaintenanceInput, workspace Workspace) (MaintenanceResult, error)
    Repair(ctx context.Context, input MaintenanceInput, report ValidationReport, previous Manifest, workspace Workspace) (MaintenanceResult, error)
}
```

DeepSeek should be one implementation of the interface.

## Workspace Tools

The hosted executor should not receive every document body in the first prompt.
It should interact with a controlled workspace.

Required tools:

```text
list_index()
search_docs(query, limit)
read_doc(path)
write_doc(path, title, summary, tags, body_md)
archive_doc(path)
finish(processed_message_ids, partial)
```

`search_docs` should use lexical search only. No embeddings should be used in
the first version.

Search should include active documents only and search these fields:

```text
path
title
summary
tags
body_md
```

Search results should return metadata and snippets, not full document bodies.
`read_doc` is the operation that consumes full-document read budget.

## Run Constraints

Constraints are part of the executor contract. They must be given explicitly to
the executor and enforced by the manager validator.

Initial suggested constraints:

```json
{
  "max_input_messages": 200,
  "max_input_message_chars": 120000,
  "max_docs_read_per_run": 20,
  "max_doc_chars_read_per_run": 160000,
  "max_output_docs": 20,
  "max_output_doc_chars_each": 64000,
  "allowed_operations": ["create_doc", "update_doc", "archive_doc", "no_op"],
  "index_is_read_only": true,
  "embedding_enabled": false
}
```

If the executor cannot process all input under the constraints, it should return
`partial=true`. A partial run is not a failure.

## Manifest

The executor should return strict JSON, not markdown diffs.

Example:

```json
{
  "partial": false,
  "processed_message_ids": ["msg_1", "msg_2"],
  "operations": [
    {
      "type": "update_doc",
      "path": "engineering/runtime-state.md",
      "title": "Runtime State",
      "summary": "Runtime state conventions.",
      "tags": ["runtime"],
      "body_md": "# Runtime State\n\n..."
    },
    {
      "type": "create_doc",
      "path": "product/team-memex.md",
      "title": "Team Memex",
      "summary": "Agent-maintained operational memory.",
      "tags": ["memex"],
      "body_md": "# Team Memex\n\n..."
    },
    {
      "type": "archive_doc",
      "path": "old/outdated.md"
    }
  ]
}
```

`no_op` with non-empty `processed_message_ids` means the executor reviewed the
messages and decided no document changes were needed.

## Manifest Validation

The manager must validate the manifest before publishing.

Recommended validation errors:

```text
SCHEMA_INVALID
UNKNOWN_OPERATION
INVALID_PATH
INDEX_WRITE_FORBIDDEN
DOC_NOT_FOUND
UPDATE_ARCHIVED_DOC
ARCHIVE_MISSING_DOC
ARCHIVED_PATH_REUSED
OUTPUT_DOC_LIMIT_EXCEEDED
DOC_BODY_TOO_LARGE
EMPTY_BODY
INVALID_PROCESSED_MESSAGES
PROCESSED_MESSAGE_NOT_IN_INPUT
```

Validation failure should produce a machine-readable validation report:

```json
{
  "retryable": true,
  "errors": [
    {
      "code": "DOC_BODY_TOO_LARGE",
      "path": "engineering/runtime.md",
      "message": "body_md is 81200 chars, limit is 64000"
    }
  ],
  "constraints": {
    "max_output_doc_chars_each": 64000
  }
}
```

The repair prompt should ask the model to return corrected JSON only. It should
include the validation report, previous manifest, and constraints.

Recommended retry limit:

```text
initial generation + 2 repair attempts
```

If all attempts fail, the run should fail and expose the final validation report
to the run status API.

## Run Logs

Run logs are internal and should be useful for debugging without appearing in
normal LLM Wiki reads.

Recommended records:

```text
team_memex_runs
- run_id
- team_id
- requested_by_user_id
- executor_type
- status
- partial
- started_at
- completed_at
- error

team_memex_run_inputs
- run_id
- message_id
- session_id
- source_agent_id
- message_created_at

team_memex_run_attempts
- attempt_id
- run_id
- attempt_number
- status
- validation_report_json
- output_manifest_json
- created_at

team_memex_run_outputs
- run_id
- document_id
- operation
- path
- created_at
```

Run statuses should include:

```text
pending
running
succeeded
partial
provider_failed
validation_failed
failed
```

## Human Read APIs

First version API surface:

```text
GET  /api/v1/user/self/teams/{team_id}/memex/index
GET  /api/v1/user/self/teams/{team_id}/memex/documents
GET  /api/v1/user/self/teams/{team_id}/memex/documents/{path}
POST /api/v1/user/self/teams/{team_id}/memex/runs
GET  /api/v1/user/self/teams/{team_id}/memex/runs/{run_id}
```

Human read APIs should return active documents only.

Human-visible document fields:

```text
path
title
summary
tags
body_md
updated_at
```

Human read APIs should not return:

```text
session_id
message_id
source_agent_id
rationale
raw run input
archived documents
```

## LLM Provider Configuration

The first version uses a manager-level hosted DeepSeek configuration. It should
not support per-team keys.

Recommended environment:

```text
MEMEX_LLM_ENABLED=true
MEMEX_LLM_PROVIDER=deepseek
MEMEX_LLM_MODEL=deepseek-v4-flash
DEEPSEEK_API_KEY_SECRET_NAME=projects/.../secrets/deepseek-api-key/versions/latest
```

The DeepSeek API key should be loaded from Google Secret Manager. It should not
be stored in the database or team config.

Use a generic resolver boundary:

```go
type SecretResolver interface {
    Resolve(ctx context.Context, ref string) (string, error)
}
```

The DeepSeek executor should receive the resolved key. It should not know how
Google Secret Manager works.

Failure behavior:

- Secret resolution failure should disable hosted memex maintenance and make
  manual runs return a provider-not-configured error.
- DeepSeek request failure should mark the run `provider_failed`.
- Exhausted manifest repair attempts should mark the run `validation_failed`.

## Security and Privacy Boundaries

First version intentionally relies on team visibility. It does not redact
secrets before hosted LLM processing.

Required boundaries:

- Only team members can read active memex documents.
- Only owner/operator can trigger manual maintenance runs.
- Run inputs come from team agents after `team_agent.joined_at`.
- Session and message references stay in internal logs.
- Archived documents stay out of index, search, run input, and normal read APIs.
- The DeepSeek API key comes from Google Secret Manager.

This means sensitive data included in team-agent messages may be sent to the
hosted LLM and may be summarized into team-visible documents. That is accepted
for v1 under the team visibility model.

## Implementation Slices

Recommended implementation order:

1. Add domain models, store interfaces, PostgreSQL tables, and memory store.
2. Add active document read APIs and generated index rendering.
3. Add manual run records and cursor storage without calling DeepSeek.
4. Add lexical search and workspace abstraction.
5. Add manifest validator and publish logic.
6. Add DeepSeek executor and Google Secret Manager-backed configuration.
7. Add validation repair loop and run attempt records.
8. Add focused service and storage tests.
9. Add a targeted integration test for team visibility and manual run behavior.

Generated artifacts should continue to come from `api/pax_manager.thrift` and
`make generate` if the API surface is added to the IDL.

## Open Questions

- Should `GET documents` support a lightweight `tag` filter in v1?
- Should manual run be synchronous or queued behind a short-lived background
  worker?
- Should `partial` be a separate terminal run status or a flag on `succeeded`?
- Should run logs be visible to owner/operator in v1 or kept internal only?

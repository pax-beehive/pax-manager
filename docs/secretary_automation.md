# Automatic Secretary

Each user can select one owned agent and enable automatic summaries at
Console `/settings/secretary`. Settings are persisted in PostgreSQL and default
to disabled. Saving the switch enables background agent execution; opening the
page alone never enables it. An offline agent can be configured and waits for
its ACP connection. The Manager does not create a replacement agent or require
a separate model API key.

## API and ownership

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/api/v1/user/self/secretary` | Selected agent, settings revision and runtime status |
| GET | `/api/v1/user/self/secretary/progress` | Current published coverage totals and paginated source sessions |
| PATCH | `/api/v1/user/self/secretary` | Save `agent_id`, `enabled`, `expected_revision` |
| POST | `/api/v1/user/self/secretary/run` | Queue pending work and explicitly retry failures; returns 202 |

Responses use the normal API envelope. Data includes `agent_id`, `enabled`,
`revision`, `state`, `pending_count`, `agent_available`, optional
`last_completed_at`, and a safe `last_error`. State is `disabled`, `idle`,
`waiting_for_agent`, `processing`, or `error`. Runtime changes never increment
the settings CAS revision. The authenticated user is always the owner; route
parameters and request bodies cannot impersonate another owner. Selection and
execution both require the agent to remain owned and active. Clearing selection
is allowed when disabled, including after an agent disappears.

The feature reads only Manager-persisted, currently owner-visible plaintext
messages. It does not ingest local native logs or infer that a native seq is a
Manager message ID. Existing E2EE ciphertext, tool output, and reasoning are not
summarized. Agent execution sessions are isolated from interactive sessions.

## Persistence and incrementality

`db/secretary.sql` adds settings, coalesced source-session jobs, per-message
processed state, and an internal-session registry. Message and text-part writes
mark work in the same PG transaction. Enabling backfills existing owned sources.
Only idle source sessions whose messages have been quiet for 30 seconds are
eligible. The worker polls every five seconds by default.

The checkpoint compares canonical message identity, full visible-text hash,
canonicalization version, and owner. It is not a message count, native seq,
search cursor, or ES acknowledgement. Late parts and edits to an older message
therefore remain detectable. Boundaries between transport parts do not create
another logical message. Metadata-only changes that leave canonical text intact
do not invoke the agent again.

Each batch contains at most 64 complete visible messages and 128 KiB of source
text. Source messages are never silently truncated to fit the model input.
Oversized batches remain failed/retryable and do not advance the checkpoint.
Previous summaries provide bounded context; they are not silently added to the
new summary's coverage or dependency graph.

New messages create source-backed fragment summary blocks. Unchanged blocks are
reused. A changed message that belongs to an existing block regenerates that
block from its current visible coverage and publishes a new revision under the
same summary ID. Old versions and their source snapshots remain immutable.
There is no extra session-summary table. This automatic block layer does not
silently migrate or rewrite the separate local Secretary topic/project board.

Publishing acquires the settings and job locks, validates the settings revision,
enabled flag, selected agent, source visibility and lease, then atomically saves
the summary, every coverage snapshot, per-message hashes and processed revision.
Failure rolls all of that back. Source changes arriving during generation remain
pending for another batch. Deletion/current ownership still governs historical
reads, as described in [search_contract.md](search_contract.md).

## Coverage progress

The progress endpoint accepts `limit` (default 20, maximum 100) and an opaque
owner-bound `cursor`. Its `counts` describe all eligible sessions, independently
of the returned page. `sessions` contains bounded session rows and `next_cursor`
continues that list. `as_of` identifies the observation time; this is a live
view, not a frozen history export. New source writes can increase pending work
or move a previously completed session back to partial coverage.

Only current owner-visible canonical plaintext user/assistant messages enter
the denominator. Empty, textless, archived, deleted, inaccessible and internal
Secretary execution sessions are excluded. Sessions with visible messages are
included before Secretary is first enabled; absence of a queue job means no
automatic published coverage, not an empty source universe.

`completed_messages` requires a successfully published checkpoint for the same
current owner, canonicalization version and full visible-body hash. Starting a
model prompt or reading source text does not advance progress. Coverage remains
accurate while Secretary is disabled, including edits and late message parts.
A multipart message counts once. A session is `completed` when all its eligible
messages match, `partial` when some match and `unstarted` when none match.

Known turns group nonempty Manager `messages.turn_id` values within each source
session. A turn completes only when every currently visible message in that
group completes. Missing turn IDs are counted as `ungrouped_messages` and never
invented from pairs of messages or sequence numbers. Console displays turns
where grouping is complete and otherwise displays precise message counts.
These counts describe currently persisted message coverage, not native ACP turn
termination; later messages or chunks can make a turn pending again.

Progress reads use a repeatable-read snapshot and one owner-scoped bulk
canonical source scan. They do not call a model or query Elasticsearch. This
MVP's read cost scales with visible source text; Console refreshes progress
every 30 seconds while the page is active, independently of settings/status
polling. A durable cached projection can replace the store implementation if
measured source volume requires it, without changing the progress contract.

## Agent execution and stopping

The normal ACP tunnel performs `session/new` and `session/prompt` on the selected
agent. Manager supplies full bounded canonical source text and requests a strict
JSON summary. Source text is untrusted data, not task instructions. The model
cannot choose the owner, summary ID, source hash, revision or coverage. Manager
reconstructs references and validates any exact evidence quotes before writing.

Internal execution sessions are registered before the first prompt and excluded
from collection and ordinary search, preventing recursive summarization and
duplicated source text in results. Their owner can still open a work session
directly for diagnostics. Normal manual permission
handling remains in force; this feature does not autoapprove agent tools.

The worker checks its settings/lease fence during execution. Disabling or changing
the selected agent cancels the active execution context and sends native
`session/cancel` when possible. Independently, the publication transaction
prevents fenced work from committing after the settings change. Already completed
summaries remain available. Disabling retains unfinished work for later use.

Stable run/turn IDs let a transient publication retry reuse an already completed
agent response. Explicit Run now/Retry rotates the durable retry generation for
inactive pending work so malformed or interrupted output can be regenerated.
The target summary identity remains stable when revising an existing block.

The old local `secretary_pipeline.py watch` prototype is not started by this
feature. The Manager worker is an independent service lifecycle, controlled by
the saved user setting. It stops before transport storage closes at shutdown.
Elasticsearch is not needed to generate or save summaries; the existing search
worker indexes current published versions when ES is available.

## Verification and deployment

No new standalone daemon is needed. Deploy the Manager with its complete `db/`
directory, then Console. `EnsureSchema` loads `secretary.sql` after search and
summary DDL. Users remain disabled until they select an agent and save an enabled
setting. For generated APIs run `make generate` from the Thrift source.

Backend checks include owner-scoped settings HTTP tests, real local WebSocket
ACP execution/cancellation/recovery tests, worker/parser tests, and PG tests for
atomic publication, late edits, immutable history, leases and fencing.

Run all feature checks with Docker running:

```bash
make secretary-acceptance
```

This creates its own PostgreSQL container on a random loopback port, rejects
skipped scenarios and removes only its own container and volume afterward.
Detailed Go JSON logs are retained in its printed private temporary directory.
The continuous-worker PG test can also run individually:

```bash
PAX_MANAGER_SECRETARY_TEST_DATABASE_URL=<isolated-postgres-dsn> \
GOWORK=off GOCACHE=/tmp/pax-manager-go-cache \
go test -tags=integration -count=1 ./internal/manager \
  -run '^TestSecretaryAutomationPostgres'
```

It uses a real HTTP server, ACP WebSocket test agent, actual PG source reads,
summary writes and checkpoints. The test agent provides deterministic model
responses; this proves the execution protocol and persistence behavior, not
the quality of a particular production model. Each run uses a fresh schema.
Unit tests also cover this scenario with the memory store.

Console browser checks are documented in its `scripts/secretary-smoke/README.md`.
They use an isolated local fixture agent, verify selection/save/reload/status and
mobile disabling, and restore the initial disabled setting. Production users or
their real agents are never selected automatically by the fixture runner.

The current deployment is a single Manager with its local agent connections.
Leases and publication fences survive restarts and coordinate database work;
multiple Manager instances will additionally need agent-aware execution routing.
This feature does not set a production latency or model-quality guarantee.

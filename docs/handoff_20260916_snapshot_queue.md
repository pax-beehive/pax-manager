# Snapshot-driven session turn queue

## Scope

Issue 028 replaces the process-local single-slot conversation queue.
Manager owns persistence and dispatch; existing paxd session runtime snapshots
drive progress. paxd currently emits change notifications and a periodic report
every 30 seconds, even while idle. No paxd or transport protocol changes are
required. Legacy agents without these snapshots do not drive this queue.

## Storage and hot path

db/init.sql adds session_turn_queue with one composite primary key
(agent_id, session_id), no historical rows or extra secondary indexes.
Input is bounded to 65536 bytes. FK deletion removes rows with their session or
author. Archived sessions and deleted agents are excluded from dispatch; their
pending records are not executed just because a daemon reports idle.

Only applied snapshots schedule work, with one check per agent at a time and
eight global check slots. The caller never waits for SQL or network I/O.
A check uses the agent prefix of the primary key and reads at most 32 rows.
No message history is queried. Queue updates and network sends are never part
of the snapshot transaction. Admission, authorization, and an atomic row claim
prevent concurrent workers from sending the same draft; claim checks the most
recent sequence/fence and compares draft contents to reject concurrent edits.

Executions have 32 slots and the service shutdown context, not the browser
request context. Saturation or busy admission leaves a pending slot for the
next paxd snapshot. An idle session may accept a queued draft, closing the old
completion/enqueue race. This can wait for the next periodic snapshot.

## Delivery and recovery

A claimed slot is sending. Current snapshots reporting the queued turn ID
confirm acceptance. Completion or an approval_required event also proves
acceptance. Conditional removal uses the turn ID, so an older runner cannot
remove a newly queued draft.

Unknown delivery is uncertain, not an automatic retry. There is no claim that
a turn ID alone provides durable exactly-once delivery. A process disappearing
between claim and confirmed acceptance leaves the input intact; subsequent
snapshots turn a sending slot older than two minutes into uncertain.
Inspect history before resending; deleting the slot does not cancel work that
paxd may have received. Normal transport replay remains owned by reliablemq.

## API and Console

Existing queue endpoints retain their paths and shape; GET additionally exposes
state (queued, sending, uncertain). Sending slots cannot be edited/deleted;
uncertain slots can be deleted but not silently overwritten. E2EE turn controls
remain unsupported. No generated Hertz/Thrift artifacts change.

Console polls a nonempty queue, labels sending/uncertain entries, and disables
unsafe edits. Observer follow-up waits up to 40 one-second retries to cover a
periodic snapshot interval. It observes execution; it does not dispatch work.

## Deployment and validation

Apply the normal Manager schema startup/migration before serving this version,
then deploy Console. Avoid mixed old/new Manager instances for queue traffic:
old versions own private in-memory slots and cannot see this table. Existing
in-memory queued drafts are not backfilled; finish or record them before restart.

Use gvm Go 1.26 with GOMAXPROCS=2 and GOFLAGS=-p=1 for this laptop.
Relevant tests cover detached dispatch, repeated idle, approval wait, coalescing,
draft replacement, concurrent claim, stale snapshot/edit rejection, and uncertain
delivery. The optional PostgreSQL test uses connection-local temporary tables:
PAX_MANAGER_QUEUE_TEST_DATABASE_URL=<isolated DSN> go test ./internal/manager/storage -run TestPostgresTurnQueue
It was run against the local PostgreSQL WASM engine through pgx without Docker.
Passed make fmt-check, make lint (including integration-tag lint),
go test -count=1 ./..., and go build -o bin/pax-manager ./cmd/manager.
Console TypeScript, scoped ESLint, and 16 relevant tests passed.
Docker-backed end-to-end tests were not run. No deployment performed.

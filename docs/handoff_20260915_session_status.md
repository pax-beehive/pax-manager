# Session runtime ownership and snapshot lock fix

## Behavior and boundaries

Paxd session_runtime.snapshot is the only production writer of the persisted
session runtime projection. ACP keeps in-memory request/turn/permission
correlation and still writes messages, tools and approvals; its projector no
longer has a storage dependency. The legacy UpdateSessionRuntimeState storage
API remains for existing internal callers/tests but has no production call site.

Sending cancel no longer persists cancelling before paxd confirms execution
state. Approval resume reuses the existing turn and request IDs; it never
allocates a replacement execution. Native permission IDs from snapshots are
resolved to Manager approval records for implicit resume. Before a snapshot
arrives, local request correlation can resume approvals or address a live
response waiter for Stop, queue and observer requests. These reads never update
the stored session runtime state.

Accepted snapshots also reconcile in-flight readers by prompt request ID, even
if Manager never received an earlier running snapshot. The existing short idle
interrupt grace allows late terminal frames to arrive. Old-fence, stale and
duplicate snapshots still cannot mutate the projection or interrupt readers.

Node disconnect no longer schedules an unknown-state rewrite. The obsolete
MarkNodeRuntimeStale API and implementations have been removed. The last runtime
state and timestamp remain intact. Console workbench badges show paxd offline
when the existing node API reports online=false, without changing the stored
execution state. Detection follows the existing node connectivity mechanism;
it is not an immediate guarantee for every network failure. Legacy unknown
values remain readable until a new snapshot or explicit reset.

## PostgreSQL locking

The snapshot agent mutex uses FOR NO KEY UPDATE, as does the retained legacy
runtime storage method. This serializes snapshots but permits the KEY SHARE
foreign-key check on messages.agent_id. Message persistence keeps its original
single statement; no new message-side agent lock or retry loop was added.
Connection-fence, sequence and session locks remain transactional.

Issue 019 is resolved in code. This removes the confirmed snapshot/message lock
cycle, not every conceivable PostgreSQL deadlock. No schema or generated API
changes are required. The preceding turn identity work is documented separately
in handoff_20260915_turn_identity.md.

## Verification

Use the installed Go 1.26 via gvm, GOMAXPROCS=2 and GOFLAGS=-p=1, with default
caches. Manager full tests passed; final affected packages and native build were
also checked. Repository-wide formatting and lint retain existing findings;
changed-code lint and integration lint are checked separately.

The optional TestPostgresSnapshotDoesNotDeadlockMessageForeignKey test accepts
PAX_MANAGER_LOCK_TEST_DATABASE_URL for an isolated database. It invokes the real
UpsertMessage and ReplaceAgentActiveTurns methods with a deterministic lock
ordering. PostgreSQL 17 passed both empty and active snapshot cases. Restoring
FOR UPDATE through a test-only Go source overlay produced SQLSTATE 40P01 in both
cases. The remote test container had one CPU and 256 MB, no production volumes
or credentials, and was removed together with its anonymous test volume.
Only the test binary was compiled for Linux using CGO_ENABLED=0; no Zig was used.

Console checks use the installed Node 24, local node_modules executables,
Vitest with one worker, TypeScript and affected-file ESLint. Badge and display
status tests cover offline display without discarding execution status.

## Rollout

No releases, production deployment or production database changes have been made. Release Manager to fix the database lock cycle. Release Console for
the offline badge. Release the accompanying paxd/Manager turn identity changes
together for canonical ordinary-ACP turn IDs. E2EE and older clients retain the
fallback identity behavior described in the turn handoff. Unrelated browser
changes in the workspace remain separate.

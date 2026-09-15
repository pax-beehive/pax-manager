# Unified ACP turn identity

## Scope

Manager-generated turn_id now travels in reliable ACP envelope metadata to
paxd, through prompt admission, and back in output envelopes and runtime
snapshots. The native ACP JSON-RPC payload and request ID allocator are unchanged.
Issue 020 is resolved for ordinary non-E2EE ACP traffic. Issue 019 is now
resolved in code with a compatible snapshot lock and real PostgreSQL regression;
see handoff_20260915_session_status.md for the subsequent status changes.

## Flow and compatibility

- Conversation prompts retain their business turn ID. Raw user tunnel prompts
  without one receive a fresh Manager ID instead of inheriting the current turn.
- Reliable journal metadata stores turn_id alongside both session IDs. Replay
  reads this stored value rather than inferring a turn from current session state.
- Paxd's runtime projector has one TurnID. Its snapshot emits turn_id and the
  legacy turn_instance_id alias with identical values. Manager accepts either,
  rejects disagreement, and exposes the same value through existing session
  runtime fields. No schema or generated API change is required.
- Tagged history uses the envelope's identity. Tool and terminal merge keys also
  include the turn so reused worker IDs cannot overwrite another turn's records.
- Tagged events for a different current turn do not change the ACP state
  projection. The subsequent status change retains only in-memory request correlation;
  ACP no longer persists session runtime state.
- A fresh execution gets a fresh ID. Route recovery before execution, permission
  continuation, connection recovery and output replay retain the original ID.

## Boundaries

Old managers and E2EE/local callers without metadata keep paxd's local fallback.
Those paths are not claimed to have unified Manager business identity. Upgrade
both Manager and paxd for the ordinary path; the alias supports rolling upgrades.

Paxd retains a bounded recent request-to-turn index for late terminal responses.
It does not add a permanent execution archive. Journal/history retention remains
unchanged. Native session-only notifications depend on ordered delivery before
the worker's terminal response; after-terminal notifications from that same
process cannot intrinsically identify the older turn without worker metadata.

No release, deployment, production data rewrite, or service restart was
performed. Existing unrelated browser work remains untouched.

## Verification

Use Go 1.26 via gvm, GOMAXPROCS=2, GOFLAGS=-p=1 and default Go caches.
Manager full tests passed; after the final history/permission changes, the
affected Manager package tests and native build passed again. New-code lint is
checked separately from the existing repository-wide format/lint findings.
Tests cover durable envelope replay metadata, canonical and legacy snapshots,
conflicting alias rejection, new IDs for raw prompts, stale tagged state updates,
and reused worker tool IDs across executions.
Paxd full tests and native build passed. Tests cover prompt admission with the Manager ID, duplicate completed-turn
rejection, original output identity, and late terminal responses during a new turn.
No Zig build or production PostgreSQL verification was performed. The subsequent
status work uses a pure-Go Linux test binary against an isolated PostgreSQL container.

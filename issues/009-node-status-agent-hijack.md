# ISSUE-009 Node status report can hijack any agent by ID (cross-tenant)

**Status:** resolved
**Severity:** blocker
**Component:** storage / auth
**Found:** 2026-07-18
**Resolved:** 2026-07-18

## Summary

`UpsertNodeStatus` upserts reported agents with `ON CONFLICT (agent_id) DO
UPDATE SET node_id = EXCLUDED.node_id` without verifying that the conflicting
agent belongs to the reporting node or its owner. A node can therefore re-home
any agent (including another user's agent) to itself, intercept the agent's
mailbox, and impersonate it.

## Round-trip affected

- [x] Agent registration / connection
- [x] User-agent messaging round-trip

## Current behavior

- `POST /api/v1/node/status` (NodeAuth) pins only the top-level
  `report.NodeID` to the authenticated node; nested `agents[].agent_id` values
  are attacker-controlled (`internal/manager/paxd/service.go:338`).
- The node control tunnel `runtime.snapshot` frame feeds the same store method
  with `cloud_agent_id` from the frame
  (`internal/manager/node_control_tunnel.go:213`).
- Postgres: the conflict branch reassigns `node_id` for any existing
  `agent_id` (`internal/manager/storage/postgres_store.go:1142`).
- Memory store: `upsertNodeAgentLocked` additionally rewrites
  `agent.OwnerUserID = node.OwnerUserID`, a full ownership transfer
  (`internal/manager/storage/memory_store.go:2326`).

Once re-homed, mailbox messages created for the victim agent are routed with
the attacker's `node_id`, which the attacker reads via `PullNodeMailbox`;
`CreateNodeOutboundMessage` and the ACP agent tunnel (`GetNodeAgent` check)
then accept the attacker as the victim agent.

Victim agent IDs leak via intended sharing (team agent listings,
representative agents), and the same-owner cross-node case needs no leak at
all. `UpsertAgentSessions` already enforces agent-on-node and has a cross-node
rejection test (`internal/manager/storage/agent_status_test.go:82`), so this
looks like an oversight rather than intent.

## Expected behavior

A status report may only create new agents or update agents that already
belong to the reporting node (or, at most, to the reporting node's owner).
Conflicting agents owned by someone else must be left untouched.

## Impact

Cross-tenant mailbox interception, outbound-message impersonation of the
victim agent, ACP tunnel hijack, and session takeover for any agent whose ID
is known. In memory-store deployments the owner's secrets and approvals are
also exposed because `owner_user_id` is rewritten.

## Affected code

```
internal/manager/storage/postgres_store.go:1125-1185 - UpsertNodeStatus agent upsert
internal/manager/storage/memory_store.go:2319-2344 - upsertNodeAgentLocked
internal/manager/paxd/service.go:338-359 - ReportNodeStatus passes agents through
internal/manager/node_control_tunnel.go:213-226 - runtime.snapshot path
```

## Proposed fix

In both stores, only adopt an existing agent when it already belongs to the
reporting node (or same owner, matching the intended migration semantics);
otherwise skip the agent (and its sessions) instead of re-homing it. Add
regression tests mirroring the existing cross-node rejection test for
`UpsertAgentSessions`.

## Resolution

Both stores now refuse to re-home existing agents via status reports.
Postgres `UpsertNodeStatus` only updates a conflicting agent when its
`node_id` and `owner_user_id` already match the reporting node; foreign
agents (and their reported sessions) are skipped. The memory store returns a
sentinel for foreign agent IDs and skips them the same way, and no longer
rewrites `OwnerUserID`. Regression tests cover cross-owner and same-owner
cross-node reports plus the happy path in
`internal/manager/storage/agent_status_test.go`.

# [ISSUE-095] Agent deletion is not reconciled between Manager and paxd

**Status:** resolved
**Severity:** high
**Component:** node control / storage / userapi
**Found:** 2026-11-19
**Resolved:** 2026-11-19

## Summary

An agent is two records: the Manager `agents` row and the paxd agent
connection that runs it. Each delete path changed one of them and nothing
brought the other in line, so both kinds of leftovers accumulated.

## Round-trip affected

- [x] Agent registration / connection
- [ ] User login / API key
- [ ] User-agent messaging round-trip

## Behavior before the fix

- `DELETE /api/v1/user/:user_id/agents/:agent_id` and the node-scoped variant
  only tombstoned the Manager row. paxd was never told, so the connection, its
  harness processes and its tunnel kept running. A node-authenticated tunnel
  dial for a tombstoned agent returns 404, which paxd treats as transient and
  retries forever. A tunnel that was already open stayed open.
- Removing the connection on the node (paxd local API, `paxctl`, or the
  Console daemon panel through `agent_connection.delete`) left the Manager row
  in place. paxd keeps reporting the removed connection in every runtime
  snapshot with `desired_state` `deleted`; Manager ignored that field and kept
  showing an offline agent that nothing could bring back.
- A runtime snapshot updated `status`, `last_heartbeat` and `metadata` of a
  tombstoned row, because the upsert did not check `deleted_at`. The row stayed
  hidden, but its tombstone state was overwritten.

Observed on a real node: six connections removed locally a month earlier still
had live Manager rows, one of which the owner later tried to delete from the
Console.

## Resolution

paxd owns its connections, and Manager follows what it reports. This applies to
the current node API (control tunnel runtime snapshots). The legacy agent API
has no connections and is unchanged.

- A runtime snapshot connection with `desired_state` `deleted` deletes the
  bound agent, unless the same snapshot has another connection for that agent
  that paxd still keeps. The deletion happens in the same transaction as the
  status update, so the runtime fence applies to it. `deleted_by_user_id` stays
  NULL because no user asked for it.
- Status reports no longer write to tombstoned rows.
- A connection that paxd still keeps for a tombstoned agent is removed: Manager
  sends `agent_connection.delete` over the control tunnel. Every snapshot
  re-evaluates this, so a node that was offline when the agent was deleted is
  cleaned up after it reconnects.
- Deleting an agent through the user API stays immediate and never depends on
  the node being reachable. When the node is connected, Manager also sends the
  removal right away, using the connection id from the last runtime snapshot.

The removal command id is derived from the agent and connection ids, so paxd
replays one command instead of recording a new one per attempt. Manager asks at
most once a minute per connection, and retries without waiting when the command
was never delivered.

## Operational notes

- After deployment the first runtime snapshot of each node deletes the agents
  whose connections were removed on that node earlier. Their sessions are
  hidden with them, as for any deleted agent. The rows are tombstones and can
  be restored by hand if one was removed by mistake.
- The first snapshot also removes connections that nodes still run for agents
  deleted earlier.
- Removing a connection and later creating a new one for the same cloud agent
  no longer works, because the agent is deleted in between. Create the new
  connection first.

## Verification

Regression coverage includes both store implementations (a PostgreSQL test runs
the real statements), the retry policy of the removal, and control tunnel tests
for: removal reported by paxd, an agent rebound to a new connection, a
connection kept for a deleted agent, deletion on a connected node, and deletion
while the node is offline followed by reconnect.

## Not covered

- `CreateNodeDaemonAgentConnection` still creates the Manager row before paxd
  accepts the connection. A rejected or undelivered create leaves a row that
  paxd never reports, which this reconciliation cannot see.
- Deleting a node tombstones its agents and revokes its key. While its control
  tunnel is still open, the next snapshot removes its connections like those
  of any deleted agent. Once the tunnel drops, paxd cannot be reached and keeps
  them. Nothing sends the removal at the moment the node is deleted.

# My Agents Discovery and Direct Message (MCP) Plan

Cross-repo plan spanning `pax-manager` and `paxd`. Scope is deliberately
trimmed from the original "My Agents" epic to what is actually needed today:
a visible agent-discovery MCP tool plus direct agent-to-agent messaging by
`agent_id`, reusing the existing conversation delivery core.

## Goal

Let an agent, running under the `pax-conversation` MCP server, do two things
without the caller having to understand representative agents:

1. Discover the agents owned by the same owner (across all nodes) through an
   MCP tool, and pick one by `agent_id`.
2. Send a direct message to that `agent_id` (including to itself, for session
   handoff), reusing the existing conversation delivery / invocation core.

The representative-agent path stays first-class and visible for future team
collaboration. It is NOT removed and NOT hidden. The `agent_id` path is an
additional addressing mode that maps `agent_id` to that agent's canonical
representative and then reuses the existing core.

## Non-goals / explicitly deferred

- E2EE / versioned message envelope, plaintext dev-mode toggle. Separate issue.
- Logical project/workspace mapping for cross-node session creation.
- Four-state reachability (offline-but-queueable / misconfigured-workspace /
  unavailable-runtime). Phase 1 surfaces only the existing heartbeat-derived
  `online` / `offline` states.
- Durable offline delivery queue and retry/idempotency machinery.
- Hiding or removing representative agents.
- Inbox model: multiple concurrent open inquiries on one target session.
  The unique constraint `idx_active_invocation_target_session ... WHERE
  status='active'` stays. Implicit `reply` (no invocation id) keeps working.
  Surfacing an explicit `ask_id` on `reply` is deferred (see "Deferred: reply
  by ask_id").
- Session discovery. `to_session_id` (Phase 2) is a caller-provided passthrough;
  there is no `list_sessions` tool.

## Key facts this plan relies on (verified in-tree)

- There are two conversation paths. The console SSE path
  `POST /api/v1/user/:userID/nodes/:nodeID/agents/:agentID/conversation`
  (`internal/manager/conversation.go`) is representative-free and is only a
  test harness here. The real target is the agent-to-agent MCP path:
  `paxd mcp conversation` -> `cloud.Client.PostConversationDelivery` ->
  manager `POST /api/v1/node/conversation/deliver`
  (`routeDeliverAgentConversation`, `route_paths.go:112`).
- The MCP is node-key authenticated (not a user principal). Owner is derived
  server-side from the calling agent: the deliver path already does
  `GetNodeAgent(node.NodeID, req.Source.AgentID)` and reads
  `sourceAgent.OwnerUserID` (`internal/manager/storage/agent_conversation.go:316`).
- Same-owner interaction is auto-allowed: `agentConversationUsersCanInteract`
  returns true when `sourceUserID == targetUserID`
  (`internal/manager/storage/agent_conversation.go:1200`, self-branch at 1208).
  Self-messaging is therefore friction-free by design.
- Owner-scoped cross-node agent listing already exists as
  `userapi.ListAgents(scope)` (`internal/manager/userapi/service.go:613`) backed
  by `store.ListAgents` (`internal/manager/storage/postgres_store.go` ~1754,
  filter `owner_user_id OR team access`). It is untyped, unpaginated, unfiltered,
  and user-principal scoped. There is no node-facing owner-scoped variant.
- The delivery core (`DeliverAgentConversation`, `conversationDeliverySpec`,
  invocation/receipt/active-invocation model) is representative-centric:
  `conversationDeliverySpec` carries `sourceRep`/`targetRep` as core fields.
  Representative ids are deterministic and idempotent:
  `deterministicRepresentativeAgentID` and `deterministicAgentProfileID`
  (`internal/manager/storage/agent_conversation.go`).
- paxd conversation execution (ACP router + slot pool: `session/new`,
  `session/prompt`, `session/resume`, native-session-id mapping via
  `daemonstore.ACPSessionRoute`, streaming back via `EmitManagerFrame`) is
  complete and needs NO change (`paxd/internal/runtime/acp_router.go`).
- Discovery source data already flows from paxd to the manager and is ingested
  into the `agents` table (control `runtime.snapshot` /
  `AgentRuntimeReport`, and HTTP `POST /api/v1/node/status` /
  `cloud.AgentStatus{Online}`, `paxd/internal/cloud/client.go:44`). No new paxd
  reporting is required for discovery.
- paxd `cloud.ConversationDeliveryTarget` already has `SessionID` and
  `InvocationID` fields; it only lacks `AgentID`
  (`paxd/internal/cloud/client.go:261`).

## Interfaces

### MCP tool: `list_agents` (Phase 1)

Registered in the `pax-conversation` MCP server alongside `ask` / `reply` /
`publish_artifact` (`paxd/cmd/paxd/mcp_conversation.go`, `conversationMCPTools()`
at line 330). Identity comes from `PAX_AGENT_ID` (the calling agent); owner is
derived server-side.

Description shown to the model:

> List the agents you own so you can pick one to message. Returns each agent's
> `agent_id` -- pass it to `ask` as `to_agent_id`. Each entry includes a display
> name, type/harness, a short description, and current reachability
> (online/offline). Filter with a free-text `query` (matches name and alias) and
> by `status`.

Input schema:

```json
{
  "type": "object",
  "properties": {
    "query":    {"type": "string"},
    "status":   {"type": "string", "enum": ["online", "offline", "any"]},
    "order_by": {"type": "string", "enum": ["relevance", "last_active", "name"]},
    "limit":    {"type": "integer"}
  }
}
```

- `status` default: `online`.
- `order_by` default: `relevance` when `query` is provided, otherwise
  `last_active`. Explicit `order_by` overrides.
- `limit` default 50, hard cap 200.

Output: a JSON array in the tool text content (structured, so the model copies
`agent_id` exactly):

```json
[
  {
    "agent_id": "agent_x",
    "name": "Backend Bot",
    "alias": "be",
    "type": "claude-code",
    "status": "online",
    "node_id": "node_1",
    "description": "maintains the API service",
    "last_active_at": "2026-07-27T10:00:00Z",
    "is_self": false
  }
]
```

`is_self` marks the calling agent (kept in results, not excluded: self-messaging
is a valid handoff flow).

### MCP tool: `ask` (Phase 1 + Phase 2)

```json
{
  "to_agent_id": "agent_x",              // Phase 1; xor to_representative_agent_id
  "to_representative_agent_id": "rep_x", // preserved, semantics unchanged
  "to_session_id": "sess_y",             // Phase 2; caller-provided passthrough
  "text": "...",
  "include_message": false
}
```

- Exactly one of `to_agent_id` / `to_representative_agent_id` is required;
  giving both or neither is an error.
- `to_agent_id` path: server resolves the agent, checks same-owner via
  `agentConversationUsersCanInteract`, maps `agent_id` to that agent's canonical
  representative (deterministic id, idempotent ensure), then runs the existing
  delivery core.
- `to_session_id` (Phase 2): present -> deliver into that specific target
  session; absent -> new session. This only applies to the `to_agent_id` path.
  The `to_representative_agent_id` path keeps its current "resolve to the
  representative's existing session" semantics unchanged.
- `PAX_REPRESENTATIVE_AGENT_ID` is no longer required for the `to_agent_id`
  path; the source's canonical representative is ensured server-side.

### MCP tool: `reply` / `publish_artifact`

Unchanged in Phase 1/2.

## Phase 1 work items

### pax-manager

1. Node-facing owner-scoped discovery endpoint.
   - New route, e.g. `GET /api/v1/node/agents` (node-key auth), taking the
     calling agent via query param (mirror the deliver path's
     `from_agent_id` / `req.Source.AgentID` convention) plus `query`, `status`,
     `order_by`, `limit`.
   - Handler resolves owner via `GetNodeAgent(node, from_agent_id)` ->
     `OwnerUserID`, then lists that owner's agents across all nodes.
   - New store method (e.g. `ListOwnerAgents(ctx, ownerUserID, filter)`) that
     reuses the `owner_user_id` scoping from `ListAgents`
     (`postgres_store.go` ~1754) but adds:
     - name/alias/description filtering (`query`),
     - effective-status filtering via `computed_status(last_heartbeat)`
       (`db/init.sql`), since status is computed at scan time, not stored,
     - ordering (`relevance` cheap CASE scoring for MVP; `last_active`;
       `name`),
     - `limit`.
   - Response is a typed payload matching the `list_agents` output shape
     (agent_id, name, alias, type, status, node_id, description,
     last_active_at, is_self). Keep it node-facing; do not reuse the untyped
     user-facing `ListAgents` map.
   - Authz: only the calling agent's owner's agents are returned. Do not leak
     other owners' agents.

2. `agent` target kind in the delivery core.
   - Add `Kind = "agent"` with an `AgentID` to the manager-side deliver request
     target (`domain.DeliverConversationRequest` / target struct; handler
     normalization near `agent_conversation_handlers.go:266`).
   - In `DeliverAgentConversation`, add an `agent` branch that:
     - resolves the target agent, verifies same owner as source via
       `agentConversationUsersCanInteract`,
     - ensures both source and target canonical representatives idempotently
       using `deterministicRepresentativeAgentID` /
       `deterministicAgentProfileID` (reuse `UpsertRepresentativeAgent`
       internals; empty approval policy for the self/same-owner case),
     - resolves/creates the target session (new session by default),
     - builds `conversationDeliverySpec` with the ensured reps and runs the
       existing `createConversationDelivery` core unchanged.
   - Keep the existing `representative` and `active_invocation` branches intact.

3. Agent alias.
   - Add a per-agent, owner-editable alias. Cheapest: store under
     `agents.user_metadata` (no migration). Cleaner: add an `alias` column plus
     a small PATCH endpoint. Alias is display/search only; it is NOT an
     addressing key (messaging uses `agent_id`), so no server-side
     alias->agent_id resolution is required.
   - Surface `alias` in the discovery payload; include it in the `query` match.

### paxd

4. `list_agents` MCP tool.
   - Register in `conversationMCPTools()` (`cmd/paxd/mcp_conversation.go:330`)
     and dispatch in `callConversationMCPTool` (line 373).
   - New `cloud.Client` method (e.g. `ListOwnerAgents`) that GETs the new
     manager discovery endpoint, passing `PAX_AGENT_ID` as the calling agent.
   - Return the manager JSON array as the tool text content.

5. `ask` accepts `to_agent_id`.
   - Add `to_agent_id` to the `ask` input schema and `callConversationMCPAsk`
     (`mcp_conversation.go:396`); enforce xor with `to_representative_agent_id`.
   - Add `AgentID` to `cloud.ConversationDeliveryTarget`
     (`internal/cloud/client.go:261`) and set `Kind: "agent"` when
     `to_agent_id` is used.
   - Drop the hard requirement on `PAX_REPRESENTATIVE_AGENT_ID` for the
     `to_agent_id` path.

## Phase 2 work items

- `ask` `to_session_id`: pass through to the manager target (paxd
  `ConversationDeliveryTarget.SessionID` already exists). Manager `agent` branch:
  present -> target that session; absent -> new session. Handle the busy-session
  case (target session already has an active invocation) by returning the
  existing actionable error.

## Deferred: reply by ask_id (future)

The manager core already supports it:
`getActiveConversationInvocationForReply(..., req.Target.InvocationID)`
(`agent_conversation.go:620`) uses an explicit invocation id when provided and
falls back to the single active invocation otherwise. To expose it later:
surface an optional `ask_id` on the MCP `reply` tool (maps to
`Target.InvocationID`), and include the invocation id in the inquiry prompt text
(`agentConversationDeliveryPrompt`, `agent_conversation_handlers.go:563`) so the
replying agent has the value to echo. Note: this id is the invocation id shown
to the target, NOT the asker's `receipt_token` (which the replier never has).
Only becomes required if the inbox model relaxes the active-invocation unique
constraint.

## Testing

- Manager unit: node discovery endpoint owner scoping (cross-owner isolation,
  stale node placement, removed agent); filter/order/limit; status computation.
- Manager unit: `agent` target branch resolves owner, ensures deterministic
  representatives idempotently (repeat calls do not duplicate), same-owner
  self-message succeeds, cross-owner without team is rejected.
- Manager unit: representative and active_invocation branches unchanged
  (regression).
- paxd unit: `list_agents` tool schema and cloud call; `ask` xor validation and
  `Kind: "agent"` wiring.
- Integration (docker): console/test-harness -> manager -> paxd -> agent ->
  manager round trip for a self-message that opens a new session, and an
  agent-to-agent message by `agent_id`.

## Risks / notes

- The discovery endpoint is a new node-facing surface authenticated by node key
  but authorized per calling agent's owner. Get the owner-derivation and
  scoping right; this is the main authz surface.
- Ordering by `relevance` in SQL: MVP uses CASE-based match scoring (exact >
  prefix > substring; name > alias > description). Upgrade to `pg_trgm`
  `similarity()` / tsvector only if needed; keep the `order_by` contract stable
  so the backend can be swapped without touching the MCP interface.
- Auto-ensuring the canonical representative must use an approval policy that
  does not block same-owner self/direct messages, or "friction-free" breaks.
- Keep code, comments, and this doc English/ASCII per AGENTS.md.

# Turn-scoped workspace search authorization

Ordinary agents can search all of their own sessions during a Manager-owned
turn without an expansion approval. Every search must explicitly declare its
agent and session range. Wider access requires a turn-scoped approval record.
This path does not add per-search access audit records. Existing approval and
tool telemetry remains.

## Explicit search scope

```json
{
  "query": "cicd",
  "scope": {"agent_ids": ["self"], "session_ids": ["*"]}
}
```

`self` resolves to the calling agent; `session_ids: ["*"]` includes all of that
agent's sessions. A concrete session list narrows that declared range. Both
scope dimensions are mandatory. The response includes the actual resolved
`scope`, including the real calling agent ID.

`agent_ids: ["*"]` means all owner-visible agents, not just the current agent,
and requires expansion approval. Concrete IDs may select other owned agents.
Unauthorized scopes return HTTP 403 with a `workspace_request_access` hint;
missing scope returns HTTP 400. Manager never silently narrows a wider request
to the current agent or to an approved subset. Session IDs inconsistent with the
declared agent range are explicitly rejected.

## Preauthorization and automatic approval

An authenticated user can attach `search_access` to a conversation prompt:

```json
{
  "session_id": "sess_example",
  "input": "Find my CI/CD deployment decisions",
  "search_access": {
    "queries": ["*"],
    "agent_ids": ["*"],
    "session_ids": ["*"]
  }
}
```

The field also travels with queued and steered prompts. Queue replacement or
editing replaces its search scope; it is persisted with the pending prompt in
`session_turn_queue.search_access`. Dispatch creates the approval for the actual
turn before sending the prompt. The Console's selection applies to one send and
is cleared after acceptance, while a failed send retains it for retry.

PAX auto approve approves an explicit `workspace_request_access` immediately.
It does not create a workspace-wide grant at turn start: the default range is
still this agent's sessions, and wider direct searches are rejected until an
expansion request is approved or the user explicitly preauthorizes the scope.
Automatic approval requires both `approval_mode: auto_approve_all` and the explicit
`permission_choice_id: pax:auto_approve`. The implicit automatic tool approval
used for agent-to-agent delivery does not grant workspace data access. Legacy
automatic mode without that explicit choice requires preauthorization, an owner
decision, or selecting PAX auto approve again.

Expansion scopes contain at most 50 entries per dimension. A sole `*` allows
any query, owner-visible agent, or session in the selected agents. Other queries are complete literal-query allowlist
entries, compared case-insensitively after whitespace normalization. Merely
including an allowed word in a larger query does not authorize that larger query.
Other agent/session values are exact IDs; `self` selects the current agent.
Missing expansion-request dimensions default to `*`;
empty lists and `*` mixed with other entries are rejected. Query entries have a
256-Unicode-character limit, session IDs 4,096, and queries at most 16 terms.
The grant's `*` is not passed to ES as a match-all query: actual search `query`
continues to use the existing literal matching contract.

## Nonblocking expansion requests

Ordinary injected conversation MCP servers expose `workspace_request_access`
and `workspace_search`. `workspace_request_access` accepts optional `queries`,
`agent_ids`, `session_ids`, and `reason`. It returns `pending`, `approved`, or `denied` with an
approval ID, turn ID, and scope. Identical requests within a turn reuse the same
record on the handling Manager. It does not wait or suspend the agent through
ACP `session/request_permission`. The agent can continue independent work and
check the request again before searching.
At most 32 distinct scope requests can be created in a turn; repeating an
existing request does not consume an additional record.

The Console polls the current turn's workspace approval records and offers
`Allow this turn` and `Deny`. These decisions do not resume an ACP permission
request. Search beyond the default range without an applicable approved record fails with HTTP 403 and
does not implicitly open an approval request. A search request never silently
expands its query or session scope.

The node transport is:

```http
POST /api/v1/node/agents/:agent_id/workspace/tools
X-Pax-Key: <node credential>
Content-Type: application/json
```

```json
{
  "session_id": "sess_example",
  "tool": "search",
  "arguments_json": "{\"query\":\"cicd\",\"scope\":{\"agent_ids\":[\"self\"],\"session_ids\":[\"*\"]},\"limit\":10}"
}
```

`tool` is `search` or `request_access`. Manager resolves the authenticated node,
agent owner, session, and active turn; callers cannot select a user or supply a
turn ID, capability, or grant to bypass that resolution. The MCP helper is
session-scoped, but no authorization is cached in it between calls. Native
permission to execute a Pax-owned MCP tool is not permission to read data.

Ordinary agents do not receive `workspace_read`, `workspace_expand`, or history
tools in this implementation. Those APIs would need matching scope enforcement
before they can be exposed through ordinary turn search grants. Secretary keeps
its separate execution capability and its existing five read tools.

## Lifetime and records

Records use domain `workspace`, operation `search`, resource type `turn`, and
the actual turn ID as `resource_ref`. `request_body` contains `turn_id` and
`scope`; the decision records whether approval came from explicit user
preauthorization or PAX auto approve. The default range does not create an
approval record. Expanded access requires a matching active turn and a valid
approval, checked again before releasing search results.

Prompt completion, prompt failure, and user cancellation expire matching records.
A local ended-turn fence also prevents new requests in the interval before the
next runtime snapshot. A 24-hour maximum lifetime is a fallback, not permission
to continue after the turn ends. Reusing a session for a new turn does not reuse
the old authorization. An SSE disconnect or a pending native tool approval alone
does not terminate the turn or revoke its authorization.

ES filters both `agent_id` and `session_id`; Manager applies the explicitly
authorized range and rechecks current source visibility and agent attribution
in PostgreSQL. Agent ranges also bind pagination cursors. Summaries without
a single indexed session are excluded by explicit session filters. Owner
isolation always applies, including when a session scope is `*`.
Team visibility of an agent does not authorize a collaborator to search its
owner's workspace; only the owner can establish the turn's search context.

## Deployment

Deploy the Manager and machine gateway policy containing the new node route,
then the updated paxd and Console. EnsureSchema adds the queue JSONB column to
existing databases. The gateway's machine route allowlist must include
`/api/v1/node/agents/:agent_id/workspace/tools` or remote calls will be rejected.
Older Managers reject the new endpoint and do not provide data access.

Encrypted sessions do not support these grants: their turn context is not owned
by Manager. The UI disables preauthorization on that transport.

Only a turn registered by Manager before dispatch can request or use access.
Node runtime snapshots alone cannot establish that context. Manager restart
invalidates the in-memory turn registration even if a persisted runtime snapshot
still says running; the next authenticated prompt establishes a new context.
Deployments must route these node requests to the Manager handling the turn,
as with the existing process-local Secretary capability. A shared multi-instance
turn registry is outside this change.

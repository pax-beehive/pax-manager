# Session Approval Plan

## Summary

The HTTP + SSE conversation path should support ACP permission requests without
requiring the frontend to hold an ACP WebSocket. Permission requests should pass
through the approval gate automatically when an existing reusable grant or
auto-approval policy applies. Only requests that need a user decision should
interrupt the current SSE stream.

The intended user experience is continuous:

1. The frontend starts or continues a conversation with `POST /conversation`.
2. The backend streams ACP updates through SSE.
3. If a permission request is already approved by policy, the backend responds
   to the agent and keeps the same SSE stream alive.
4. If user approval is required, the backend creates an approval record, emits
   an approval-required event, then ends the SSE stream.
5. The frontend records the user decision with the approval API.
6. The frontend opens a new conversation SSE request in resume mode.
7. The backend sends the saved permission response to the agent and streams the
   rest of the original turn.

## Existing Building Blocks

The database already has a request-and-decision record:

- `agent_approvals`

This single table stores the request side, the user decision side, and reusable
grant fields. It already supports:

- request origin: `request_node_id`, `request_agent_id`,
  `request_session_id`, and the native ACP request id. The target field name
  for this plan is `native_id`; the current schema equivalent is
  `source_message_id`.
- request metadata: `domain`, `operation`, `resource_type`, `resource_ref`,
  `title`, `description`, `risk_level`, `action_fingerprint`
- request payloads: `request_body`, `requested_effects`, `options`,
  `raw_payload`
- decision state: `status`, `decision`, `decision_option`,
  `decision_scope`, `decided_by_user_id`, `decided_at`
- reusable grants: `grant_node_id`, `grant_agent_id`, `grant_session_id`,
  `grant_revoked_at`, `grant_revoked_by_user_id`,
  `grant_revocation_reason`

The backend already has store methods for:

- `CreateApproval`
- `GetApproval`
- `ListApprovals`
- `DecideApproval`
- `FindReusableApprovalGrant`
- `ListApprovalGrants`
- `RevokeApprovalGrant`

The ACP pipeline already has approval middleware that can detect
`session/request_permission`, check reusable grants, and auto-respond to the
agent with an allow-once result when a grant applies.

## Product Decision

The conversation stream should not always stop at a permission request.

If the approval gate can be passed automatically, the SSE stream should continue
without surfacing an approval interruption to the frontend.

If user approval is required, the SSE stream should end at a durable interrupt.
The user decision should be a normal HTTP response, and the continuation should
be a new SSE request in resume mode.

This matches mature streaming agent patterns where tool approval produces an
interrupt/resume boundary rather than keeping the original stream open while a
human may take an arbitrary amount of time to decide.

## ACP Reference

The canonical ACP reference for this plan is:

- GitHub repository:
  <https://github.com/agentclientprotocol/agent-client-protocol>
- ACP v1 tool-call permission docs:
  <https://agentclientprotocol.com/protocol/v1/tool-calls#requesting-permission>
- ACP v1 JSON schema:
  <https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/main/schema/v1/schema.json>

Important reference facts:

- ACP uses JSON-RPC 2.0 envelopes.
- `session/request_permission` is an agent-to-client request.
- The request params are `sessionId`, `toolCall`, and `options`.
- `toolCall` is a `ToolCallUpdate` and must include `toolCallId`; other fields
  such as `kind`, `status`, `title`, `content`, `locations`, `rawInput`, and
  `rawOutput` are optional.
- `options` is an array of `PermissionOption`.
- `PermissionOption` requires `optionId`, `name`, and `kind`.
- ACP v1 `PermissionOptionKind` values are `allow_once`, `allow_always`,
  `reject_once`, and `reject_always`.
- The response to `session/request_permission` is not the selected option
  object itself. The JSON-RPC result must be a `RequestPermissionResponse`:

```json
{
  "outcome": {
    "outcome": "selected",
    "optionId": "allow-once"
  }
}
```

- If the prompt turn is cancelled, the result uses:

```json
{
  "outcome": {
    "outcome": "cancelled"
  }
}
```

Implementation note: current pax-manager code has legacy helper behavior that
returns the selected option object directly. The implementation should be
updated toward the official ACP v1 response shape, or compatibility should be
explicitly handled if paxd still expects the legacy shape.

## Approval Gateway Policy

Pax-manager should own reusable approval policy. ACP should only receive the
current-turn permission outcome.

This means the frontend can display broader approval choices such as:

- allow once
- allow for this session
- allow always for this agent
- allow always on this node
- allow always on all agents
- deny

But the response sent back to ACP should still represent a current-turn
selection. For allow decisions, send the ACP allow-once option id. For deny
decisions, send the ACP reject-once option id.

The broader choice is saved in `agent_approvals` as manager policy and audit
state. Future matching `session/request_permission` frames are intercepted by
the approval gateway and auto-approved before they reach the frontend.

Practical consequences:

- The frontend directly approving without long-term policy is `allow_once`.
- Session-scoped, agent-scoped, node-scoped, and all-agent choices do not teach
  ACP a durable policy.
- Session-scoped, agent-scoped, node-scoped, and all-agent choices create
  reusable manager grants.
- Later matching permission requests are handled by manager auto-approval.
- Auto-approved requests keep the same SSE stream alive and do not interrupt.
- Audit records live in manager storage, not in ACP.

## Conversation API Shape

Normal prompt:

```json
{
  "input": "run tests"
}
```

Resume from the current session interruption:

```json
{
  "session_id": "sess_...",
  "resume": true
}
```

Optional explicit resume form, useful for debugging or future ambiguity:

```json
{
  "session_id": "sess_...",
  "resume": {
    "approval_id": "appr_..."
  }
}
```

Recommended first-version frontend path:

1. Prefer the explicit resume object with `approval_id` after the frontend has
   observed an `approval_required` or `interrupted` event.
2. Keep `resume: true` as a permissive resume mode for refresh, reconnect,
   debugging, and older frontend clients.
3. When `resume: true` is used, let the backend infer the current blocking
   approval for the session.

Validation rules:

- `input` and `resume` are mutually exclusive.
- `input` is required for a normal prompt.
- `session_id` is required for resume.
- Resume requires an existing manager session that belongs to the requested
  node and agent.
- Resume must find exactly one current resumable approval for that session,
  unless an explicit `approval_id` is provided.
- The approval must belong to the same user, node, agent, and session.
- The approval must already be decided.
- If an explicit `approval_id` is provided, backend validation still owns
  correctness; the frontend-provided id only narrows the candidate approval.
- Resume does not create a new user message.
- Resume does not send `session/prompt`.
- Resume sends the permission response to the agent and then streams the
  original turn until completion or another interrupt.

## Wire Schema

This section is the contract for the first implementation. Field names are
JSON field names.

### Conversation Request

Current request fields:

```json
{
  "session_id": "sess_...",
  "input": "run tests"
}
```

Target request fields:

```json
{
  "session_id": "sess_...",
  "input": "run tests",
  "resume": false
}
```

Field rules:

- `session_id`: optional for a new prompt, required for resume. This is always
  the manager session id, not the native ACP session id.
- `input`: optional string. Required when `resume` is absent or false.
- `resume`: optional boolean or object. If true, resume from the current
  approval interrupt for `session_id` using backend inference. If object, it
  should include `approval_id` when the resume was triggered by a user approval
  decision.

Explicit resume object:

```json
{
  "session_id": "sess_...",
  "resume": {
    "approval_id": "appr_..."
  }
}
```

`approval_id` is preferred in the main approval-driven frontend path. If
present, it narrows the resume target. If absent, the backend infers the current
blocking approval for the session. Do not add a free-form resume reason for
correctness; the structured `approval_id`, session ownership checks, and
approval status checks are the source of truth.

### Conversation SSE Envelope

Current envelope shape:

```json
{
  "type": "acp",
  "node_id": "node_...",
  "agent_id": "agent_...",
  "session_id": "sess_...",
  "frame": {}
}
```

Target envelope shape:

```json
{
  "type": "approval_required",
  "node_id": "node_...",
  "agent_id": "agent_...",
  "session_id": "sess_...",
  "approval_id": "appr_...",
  "approval": {},
  "frame": {},
  "message": "",
  "reason": ""
}
```

Field rules:

- `type`: required string. Known values are `session`, `acp`,
  `approval_required`, `interrupted`, `done`, and `error`.
- `node_id`: optional string. Present for user-visible conversation events.
- `agent_id`: optional string. Present for user-visible conversation events.
- `session_id`: optional string. Present after the manager session is known.
- `frame`: optional object. Present for `acp` and `approval_required` events
  when the frontend needs the ACP frame.
- `approval_id`: optional string. Present for approval interruption events.
- `approval`: optional object. A serialized `AgentApproval` record. Present for
  `approval_required` when available.
- `message`: optional string. Present for `error` and may be used for human
  readable interruption details.
- `reason`: optional string. Present for `interrupted`. For permission
  interrupts, use `permission_required`.

### ACP Permission Request Frame

Agent-to-manager raw ACP frame:

```json
{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "session/request_permission",
  "params": {
    "sessionId": "native-or-manager-session",
    "toolCall": {
      "toolCallId": "toolu_...",
      "kind": "execute",
      "title": "go test ./...",
      "rawInput": {
        "command": "go test ./..."
      }
    },
    "options": [
      {
        "optionId": "allow",
        "kind": "allow_once",
        "name": "Allow"
      },
      {
        "optionId": "reject",
        "kind": "reject_once",
        "name": "Reject"
      }
    ]
  }
}
```

Manager-forwarded manual approval frame:

```json
{
  "jsonrpc": "2.0",
  "id": 12,
  "method": "session/request_permission",
  "params": {
    "sessionId": "sess_...",
    "approvalId": "appr_...",
    "approval_id": "appr_...",
    "toolCall": {},
    "options": [
      {
        "optionId": "allow",
        "kind": "allow_once",
        "name": "Allow"
      },
      {
        "optionId": "reject",
        "kind": "reject_once",
        "name": "Reject"
      },
      {
        "optionId": "allow_always_on_all_agents",
        "kind": "allow_always",
        "name": "Allow always on all agents",
        "_meta": {
          "paxDecisionOption": "allow_always_on_all_agents",
          "paxDecisionScope": "across_all_agents"
        }
      }
    ]
  }
}
```

Frame rules:

- The `id` is the original ACP permission request id and must be preserved for
  the eventual JSON-RPC response.
- The forwarded `sessionId` must be the manager `sess_*` id.
- `approvalId` and `approval_id` both refer to the manager approval id. Use
  both to reduce frontend shape ambiguity.
- `options` must include all original agent options plus manager-added reusable
  grant options.
- Manager-added ACP options must still use ACP-valid `kind` values. For example,
  `allow_always_on_all_agents` should be an `optionId`, while `kind` should be
  `allow_always`.
- Manager-specific semantics should go in `_meta`, not in ACP enum fields.
- Manager-added reusable options are UI and audit inputs for pax-manager. Even
  when selected, the ACP response should normally use the current-turn allow
  option id, while manager stores the reusable grant for future auto-approval.
- Auto-approved frames should not be forwarded to the frontend.

### Approval Record Mapping

When manual approval is required, create `agent_approvals` from the ACP frame:

```json
{
  "approval_id": "appr_...",
  "native_id": "perm_1",
  "owner_user_id": "user_...",
  "request_node_id": "node_...",
  "request_agent_id": "agent_...",
  "request_session_id": "sess_...",
  "domain": "agent_action",
  "operation": "session/request_permission",
  "resource_type": "acp_permission",
  "resource_ref": "toolu_...",
  "title": "go test ./...",
  "description": "",
  "risk_level": "unknown",
  "action_fingerprint": "acp:tool_call:execute:go test ./...",
  "request_body": {},
  "requested_effects": [],
  "options": [],
  "status": "pending",
  "raw_payload": {}
}
```

Mapping rules:

- `request_session_id`: manager session id.
- `native_id`: the original ACP JSON-RPC request id for
  `session/request_permission`, such as `perm_1` or `12`. This is intentionally
  distinct from the manager `approval_id`. This approval `native_id` is scoped
  to the permission request and should not be confused with an agent session's
  native ACP session id.
- `domain`: `params.domain` when present, otherwise `agent_action`.
- `operation`: `params.operation` when present, otherwise
  `session/request_permission`.
- `resource_type`: `acp_permission`.
- `resource_ref`: prefer `params.toolCall.toolCallId`; otherwise use the ACP
  request id string.
- `title`: prefer `params.toolCall.title`; otherwise use the operation.
- `description`: prefer `params.toolCall.rawInput.description` when present.
- `risk_level`: `params.riskLevel` or `params.risk_level` when present,
  otherwise `unknown`.
- `action_fingerprint`: use `params.action_fingerprint`,
  `params.actionFingerprint`, stable tool call fingerprint, or stable hash of
  params excluding `options`.
- `request_body`: the normalized `params` object without `options` if possible.
- `requested_effects`: empty array for the first version unless ACP supplies a
  structured effect list.
- `options`: normalized manager approval options. Must include decision options
  for deny, allow once, allow for this session, allow for this agent, allow
  always on this node, and allow always on all agents.
- `raw_payload`: the full manager-visible forwarded ACP frame, including the
  original request id and final options.

### Runtime State Mapping

Session runtime state should use the manager approval id as the user-facing
interrupt id:

- `PendingApprovalID`: the manager `approval_id`, such as `appr_...`.
- `BlockedRef`: the user-facing tool call reference when available, otherwise
  the manager `approval_id`.
- `native_id`: the ACP JSON-RPC permission request id, such as `perm_1`. This
  is stored on the approval row and used only when sending the JSON-RPC
  response back to the agent.

When a manual permission request creates an approval row, store the ACP request
id in `native_id`, then update session runtime state with
`PendingApprovalID = approval.approval_id`. Resume inference for `resume: true`
can then read the current manager approval id directly from session runtime
state and require that the approval is decided and belongs to the same user,
node, agent, and manager session.

### Approval Decision Request

Existing approval decision request:

```json
{
  "decision_option": "allow_once",
  "reason": "",
  "grant_node_id": "",
  "grant_agent_id": "",
  "grant_session_id": "",
  "grant_body": {}
}
```

Field rules:

- `decision_option`: required for normal UI decisions. Known first-version
  values are `deny`, `allow_once`, `allow_for_this_session`,
  `allow_for_this_agent`, `allow_always_on_this_node`, and
  `allow_always_on_all_agents`.
- When a decision comes from an ACP option, `decision_option` should be mapped
  from the option id or `_meta.paxDecisionOption`.
- `reason`: optional audit text.
- `grant_*`: only used for custom grant scopes.
- `grant_body`: optional JSON metadata for the saved grant.

The decision endpoint records the decision only. It does not send the ACP
response to the agent.

Decision storage mapping:

| decision_option | decision | decision_scope | grant_node_id | grant_agent_id | grant_session_id |
| --- | --- | --- | --- | --- | --- |
| `deny` | `deny` | `once` | request node id | request agent id | request session id |
| `allow_once` | `allow` | `once` | request node id | request agent id | request session id |
| `allow_for_this_session` | `allow` | `session` | request node id | request agent id | request session id |
| `allow_for_this_agent` | `allow` | `agent` | request node id | request agent id | `*` |
| `allow_always_on_this_node` | `allow` | `node` | request node id | `*` | `*` |
| `allow_always_on_all_agents` | `allow` | `across_all_agents` | `*` | `*` | `*` |

Only rows with `decision = allow` and `decision_scope != once` are reusable
manager grants for future auto-approval.

### ACP Permission Response Frame

Resume sends this manager-to-agent ACP frame:

```json
{
  "jsonrpc": "2.0",
  "id": 12,
  "result": {
    "outcome": {
      "outcome": "selected",
      "optionId": "allow"
    }
  }
}
```

Response rules:

- `id`: copied from the original `session/request_permission` request.
- `result`: an ACP `RequestPermissionResponse`.
- If `decision_option` is `deny`, set `outcome.outcome = "selected"` and use a
  reject option id from `params.options`.
- If `decision_option` is `allow_once`, set `outcome.outcome = "selected"` and
  use an allow-once option id from `params.options`.
- If `decision_option` is any allow choice, including reusable manager choices
  such as `allow_for_this_session`, `allow_for_this_agent`,
  `allow_always_on_this_node`, or `allow_always_on_all_agents`, save the
  manager grant according to the decision mapping but send an agent-valid
  selected outcome with the original allow-once option id as the current-turn
  ACP result.
- The response must pass through the user-to-agent ACP pipeline so runtime state
  observes permission resolution.

## SSE Behavior

### Auto-Approved Permission

When the agent emits `session/request_permission` and a reusable grant or
auto-approval policy applies:

1. The backend builds a JSON-RPC response for that permission request.
2. The backend sends the response to the agent.
3. The original SSE stream remains open.
4. The frontend does not need to show an approval UI.

The first version should avoid adding a frontend-visible event for this case
unless the UI explicitly needs audit decorations.

### Manual Approval Required

When no reusable grant or auto-approval policy applies:

1. The backend creates an `agent_approvals` row with `status = pending`.
2. The backend emits an SSE event such as:

```json
{
  "type": "approval_required",
  "node_id": "node_...",
  "agent_id": "agent_...",
  "session_id": "sess_...",
  "approval": {
    "approval_id": "appr_..."
  },
  "frame": {
    "jsonrpc": "2.0",
    "id": 12,
    "method": "session/request_permission",
    "params": {}
  }
}
```

3. The backend emits a terminal interrupt event such as:

```json
{
  "type": "interrupted",
  "reason": "permission_required",
  "node_id": "node_...",
  "agent_id": "agent_...",
  "session_id": "sess_...",
  "approval_id": "appr_..."
}
```

4. The backend ends the SSE stream.

The frontend then calls:

```http
POST /api/v1/user/:user_id/approvals/:approval_id/decision
```

That decision endpoint should only record the decision. It should not continue
the agent turn. The frontend should then call conversation resume to open a new
SSE stream.

## Resume Behavior

Resume should:

1. Resolve the session and claim the live agent tunnel.
2. Find the current decided approval for the session. If `resume.approval_id`
   is present, use it as a narrowing key. If the request uses `resume: true`,
   infer the approval from current session runtime state and approval storage.
3. Reconstruct the ACP permission response from the stored request frame and
   the saved decision.
4. Send that JSON-RPC response to the agent through the user-to-agent ACP
   pipeline.
5. Subscribe to agent output and stream updates through SSE.
6. Finish with `done`, `error`, or another `interrupted` event.

The JSON-RPC response shape should be:

```json
{
  "jsonrpc": "2.0",
  "id": "<approval.native_id>",
  "result": {
    "outcome": {
      "outcome": "selected",
      "optionId": "<allow-once-or-reject-once-option-id>"
    }
  }
}
```

If the user selects a manager-specific reusable grant option such as
`allow_always_on_all_agents`, the backend should still send a valid current-turn
allow result to the agent. In practice, that means using the original
allow-once ACP option id for the agent response while saving the reusable grant
semantics in manager storage.

Resume idempotency should be handled by the backend. If the selected approval
has already produced an ACP response, resume should not send a duplicate
JSON-RPC response to the agent. It should either continue streaming from the
live session when that is still possible or return a conflict/error that is
safe for the frontend to surface.

## Data Model Notes

The existing `agent_approvals` table is enough to record request and decision.
Rename or add the native ACP request-id column as `native_id` for this workflow;
it replaces the older `source_message_id` naming in the approval API contract.

For resume idempotency, add response-tracking fields:

- `responded_at`
- `response_body`
- `response_error`

These fields record whether pax-manager has already sent the saved permission
decision back to the agent. They prevent duplicate JSON-RPC responses when a
frontend retries resume after a disconnect, refresh, or timeout.

## First Version Scope

Implement:

- auto-approval pass-through keeps SSE alive
- manual approval creates an approval record
- manual approval emits `approval_required` and `interrupted`
- conversation supports explicit `resume.approval_id`
- conversation supports permissive `resume: true`
- resume infers the current blocking approval from session state or approval
  storage
- resume records permission-response delivery with `responded_at`,
  `response_body`, and `response_error`
- resume sends the saved permission response and continues streaming

Defer:

- durable replay of already-sent permission responses
- multiple simultaneous pending permission requests per session
- explicit run/interruption resources
- frontend-visible audit events for auto-approved permissions
- complex cross-device arbitration beyond normal approval ownership checks

## Test Plan

Use testify style assertions and BDD-style test names where practical.

Backend tests should cover:

- Given a reusable approval grant when the agent requests permission, then the
  same conversation SSE stream continues and no approval-required event is
  emitted.
- Given no reusable approval grant when the agent requests permission, then an
  approval record is created and the SSE stream ends with `interrupted`.
- Given a decided approval when the frontend resumes the session, then the
  backend sends the permission response and streams the remaining agent output.
- Given an undecided approval when the frontend resumes, then the backend
  returns `409`.
- Given a session with no current approval interruption when the frontend
  resumes, then the backend returns `409`.
- Given a manager-specific reusable grant decision, then resume sends an
  ACP v1 selected outcome with the original allow-once option id while
  preserving the reusable grant record.

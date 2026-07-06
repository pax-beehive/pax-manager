# A2A Invocation Message Display Contract

## Summary

Agent-to-agent invocation history should preserve the real session transcript
while also exposing a stable display message for the console.

The design intentionally avoids new database columns. It uses the existing
`messages` fields:

- `message_type`
- `parent_message_id`
- `raw_json`

## Core Contract

Real transcript messages keep their real `direction`, `role`, and `status`.

Use the `pax:` message type prefix only when Pax synthesized, wrapped, or
injected the message. Do not add the prefix merely because a message is related
to a Pax invocation.

Examples:

- A normal user prompt remains `user_message`.
- A Pax-wrapped prompt injected into an agent session is `pax:user_message`.
- Agent-emitted `tool_call` and `tool_call_update` messages remain
  `tool_call` and `tool_call_update`.
- A display replacement message is `pax:invocation`.

For display messages:

- `message_type = "pax:invocation"`
- `parent_message_id` points to the real message position where the display
  message should render.
- `raw_json.replaces_message_ids` lists the real messages hidden in normal
  transcript view.
- Other invocation details live in `raw_json`.

Frontend normal view rule:

1. For each `pax:invocation` message, render it at the position of
   `parent_message_id`.
2. Hide all message ids in `raw_json.replaces_message_ids`.
3. If `replaces_message_ids` is absent or empty, hide only `parent_message_id`.
4. Debug/details views may still reveal the replaced real transcript messages.

## Direction And Role

Do not use `agent_to_agent` as a `messages.direction` value for invocation
history.

`messages.direction` should describe the flow inside the current session:

- `user_to_agent`
- `agent_to_user`

A2A semantics belong in `message_type` and `raw_json`, not in `direction`.

## Source-Side Tool Call Case

On the source side, an inquiry or reply may be initiated through a tool call.
The real transcript can include several messages:

- `tool_call`
- one or more `tool_call_update` messages
- permission request or approval messages interleaved with the tool call
- a terminal `tool_call_update` when the operation completes

The `pax:invocation` display message should usually use the terminal completed
message as `parent_message_id`, because the display message only becomes final
after completion.

Its `raw_json.replaces_message_ids` may hide the whole tool-call group in normal
view while preserving the real messages for replay and debugging.

Example:

```json
{
  "message_type": "pax:invocation",
  "parent_message_id": "msg_tool_call_update_completed",
  "raw_json": {
    "invocation_id": "inv_123",
    "invocation_type": "agent_conversation",
    "phase": "inquiry",
    "side": "source",
    "replaces_message_ids": [
      "msg_tool_call",
      "msg_tool_call_update_pending",
      "msg_tool_call_update_completed"
    ],
    "sender": {
      "agent_id": "agent_a",
      "session_id": "sess_a"
    },
    "receiver": {
      "agent_id": "agent_b",
      "session_id": "sess_b"
    },
    "content": {
      "display_text": "Asked Agent B to review the request contract.",
      "original_text": "Please review the request contract."
    }
  }
}
```

## Target-Side Injected Prompt Case

On the target side, Pax injects a wrapped user prompt into the target agent
session. The real transcript message should keep the session-local flow:

- `direction = "user_to_agent"`
- `role = "user"` if the runtime sees it as a user prompt
- `message_type = "pax:user_message"`

The display message is `pax:invocation` and points to that real injected prompt.

Example:

```json
{
  "message_type": "pax:invocation",
  "parent_message_id": "msg_real_inquiry_target",
  "raw_json": {
    "invocation_id": "inv_123",
    "invocation_type": "agent_conversation",
    "phase": "inquiry",
    "side": "target",
    "replaces_message_ids": ["msg_real_inquiry_target"],
    "sender": {
      "agent_id": "agent_a",
      "session_id": "sess_a"
    },
    "receiver": {
      "agent_id": "agent_b",
      "session_id": "sess_b"
    },
    "content": {
      "display_text": "Agent A asked you to review the request contract.",
      "original_text": "You are receiving a Pax conversation inquiry from another agent..."
    }
  }
}
```

## Reply Legs

The same contract applies to replies.

When the replying agent emits the reply through a tool call, keep the real
agent-emitted tool messages unprefixed and use a `pax:invocation` display
message on the source side of that reply.

When Pax wraps the reply and injects it back into the original source agent
session, store the real injected prompt as `pax:user_message` and add a
`pax:invocation` display message pointing to it.

## Implementation Notes

- `message_id` remains opaque. Do not encode source, target, phase, or side in
  `message_id`.
- `logical_key` may be deterministic for idempotency, but it is not a user
  display contract.
- `raw_json` should contain invocation metadata such as:
  - `invocation_id`
  - `invocation_type`
  - `phase`
  - `side`
  - `sender`
  - `receiver`
  - `content.display_text`
  - `content.original_text`
  - `replaces_message_ids`
- `parent_message_id` is the display position and causal parent for
  `pax:invocation`.
- `raw_json.replaces_message_ids` controls what normal transcript view hides.


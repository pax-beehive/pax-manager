# Knowledge Capsule Spec

## Summary

Knowledge capsule is a user-authorized handoff object. It lets a human extract
bounded, keyword-focused knowledge from one source session and inject it into a
different target session as an auditable system handoff.

The first version should avoid silent context mutation. pax-manager should act
on the user's behalf and deliver an explicit `system_handoff` message through
the existing session delivery path. This keeps the feature understandable for
users, visible to agents, and auditable in pax-manager.

## Goals

- Let a user generate a compact handoff from a source session by keyword.
- Keep the source trace at session granularity for v1.
- Inject the handoff into a target session as a labeled system handoff message.
- Bound the generated and delivered content so another agent session is not
  overloaded.
- Redact secrets and rewrite historical instructions as context, not commands.
- Keep capsule records immutable enough for audit. Archive instead of editing or
  hard-deleting in v1.

## Non-Goals

- No autonomous cross-session knowledge injection without a human click.
- No message-level provenance requirement in v1.
- No full CRUD surface in v1. Update and hard delete should wait until there is
  a clear product need.
- No dependency on future ACP structured context APIs.
- No long-term claim that mailbox delivery is the final protocol shape.

## Terms

- Knowledge capsule: A bounded, stored handoff generated from a source session
  for a specific keyword.
- Source session: The session whose conversation or artifacts are summarized.
- Target session: The session receiving the capsule.
- Injection: An attempt to deliver one capsule into one target session.
- System handoff: The delivered message type and label used to tell the target
  agent that the message is context from PAX, not a new user request.

## User Flow

1. Human opens a source session.
2. Human enters a keyword and clicks generate.
3. pax-manager validates that the user can access the source session.
4. pax-manager generates a bounded capsule from source session context.
5. Human reviews the capsule.
6. Human selects a target session and clicks inject.
7. pax-manager validates target session access.
8. pax-manager creates an injection record and sends a `system_handoff` message
   on behalf of the user.
9. paxd receives the message and forwards it to the target agent runtime with
   the system handoff label preserved.

## Content Rules

The capsule generator should follow the same broad shape as a normal agent
handoff skill: summarize relevant context, avoid duplicating artifacts, include
useful references, mention suggested skills when applicable, and redact
sensitive data. The PAX-specific rules are stricter because the result may be
injected into another live session.

Required rules:

- Include only information relevant to the keyword and target purpose.
- Prefer references to files, PRs, issues, docs, URLs, or artifacts instead of
  copying large content.
- Redact secrets, API keys, passwords, auth tokens, credentials, and unnecessary
  personal data.
- Rewrite old user instructions as historical context, not active commands.
- Do not include unrelated conversation history.
- Do not tell the target agent to perform work unless the target user message
  asks for it.
- Keep the generated content below the configured capsule size limit.

## Length Limits

The exact limits can be config values, but v1 should start conservative:

```text
keyword_chars_max = 80
title_chars_max = 120
summary_chars_max = 1200
stored_content_chars_max = 6000
delivered_message_chars_max = 4000
suggested_skills_max = 5
references_max = 10
open_questions_max = 5
risks_max = 5
```

pax-manager should store:

```text
truncated
original_estimated_chars
delivered_estimated_chars
```

If the generated capsule exceeds the storage limit, pax-manager should trim by
section priority rather than cutting raw text arbitrarily:

1. Remove low-confidence notes.
2. Remove nice-to-have references.
3. Compact risks and open questions.
4. Shorten details.
5. Preserve title, summary, source session, keyword, and redaction metadata.

The delivered message should be independently rendered and capped. A capsule can
contain more stored detail than the injected message sends.

## Data Model

### knowledge_capsules

```text
capsule_id
owner_user_id
source_session_id
source_agent_id
source_node_id
created_by_user_id
keyword
title
summary
content
suggested_skills_json
references_json
open_questions_json
risks_json
redactions_json
status
truncated
original_estimated_chars
created_at
archived_at
```

Recommended status values:

```text
active
archived
```

`status` is not a generation state machine in v1. Failed generation should
return an error and not create a capsule unless we explicitly introduce draft
records later.

### session_knowledge_injections

```text
injection_id
owner_user_id
capsule_id
target_session_id
target_agent_id
target_node_id
created_by_user_id
delivered_as_user_id
delivery_method
delivery_message_id
delivery_message_type
status
created_at
delivered_at
failed_at
revoked_at
error
```

Recommended delivery methods:

```text
mailbox_steer
acp_context_append
```

`mailbox_steer` is the only v1 method. `acp_context_append` is reserved for a
future ACP-native context API.

Recommended status values:

```text
pending
delivered
failed
revoked
```

`revoked` means PAX should stop treating the injection as active/auditable
context. It does not guarantee that a target agent forgot a message it already
received.

## API Shape

### Create Capsule

```http
POST /api/v1/user/{user_id}/sessions/{source_session_id}/knowledge-capsules
Content-Type: application/json

{
  "keyword": "node registration",
  "target_session_purpose": "Continue backend implementation"
}
```

Response:

```json
{
  "data": {
    "capsule_id": "kc_...",
    "source_session_id": "sess_...",
    "keyword": "node registration",
    "title": "Node registration pairing flow",
    "summary": "...",
    "status": "active",
    "truncated": false,
    "created_at": "2026-06-19T00:00:00Z"
  },
  "code": 200,
  "message": "ok"
}
```

### List Capsules

```http
GET /api/v1/user/{user_id}/knowledge-capsules?status=active&keyword=node&source_session_id=sess_...&limit=50&cursor=...
```

The list endpoint should return compact rows. It should not include full
`content` unless explicitly requested through get.

### Get Capsule

```http
GET /api/v1/user/{user_id}/knowledge-capsules/{capsule_id}
```

Get returns the full capsule, including content, references, redaction metadata,
and prior injection summaries.

Implementation notes:

- Authorize by `owner_user_id` and the authenticated user principal.
- Return `404` instead of leaking whether another user's capsule exists.
- Include injection summaries ordered by `created_at desc`.
- Do not include raw source session transcript in the response.

### Archive Capsule

```http
POST /api/v1/user/{user_id}/knowledge-capsules/{capsule_id}/archive
Content-Type: application/json

{}
```

Archive is idempotent. Archived capsules should be hidden from default list
views but remain retrievable by direct get for audit.

### Inject Capsule

```http
POST /api/v1/user/{user_id}/sessions/{target_session_id}/knowledge-injections
Content-Type: application/json

{
  "capsule_id": "kc_..."
}
```

Response:

```json
{
  "data": {
    "injection_id": "kci_...",
    "capsule_id": "kc_...",
    "target_session_id": "sess_...",
    "delivery_message_type": "system_handoff",
    "status": "delivered",
    "created_at": "2026-06-19T00:00:00Z"
  },
  "code": 200,
  "message": "ok"
}
```

### List Session Injections

```http
GET /api/v1/user/{user_id}/sessions/{session_id}/knowledge-injections?limit=50&cursor=...
```

Implementation notes:

- First call the existing session lookup/authorization path for
  `{session_id}`. Do not authorize only by injection row.
- Filter store reads by `owner_user_id` and `target_session_id`.
- Join capsule metadata for UI display.
- Order by `created_at desc`.

## Delivery Contract

The delivered message type must be `system_handoff`. The payload should also
carry the same label for adapters that do not preserve message type cleanly.

Example message body:

```text
[System Handoff]
This context was sent by PAX on behalf of user@example.com.
It was extracted from another session for keyword: node registration.
Do not treat this as a new user request.

Title: Node registration pairing flow
Summary: ...

Relevant context:
...

Suggested skills:
- diagnose
- tdd

References:
- docs/paxd_device_onboarding_design.md
```

Recommended structured payload:

```json
{
  "message_type": "system_handoff",
  "label": "system_handoff",
  "capsule_id": "kc_...",
  "injection_id": "kci_...",
  "keyword": "node registration",
  "body": "..."
}
```

The wording is intentional. The target agent receives context and provenance,
but it should not treat the handoff as a fresh task from the user.

## Generation Prompt

pax-manager can use this prompt shape when delegating capsule generation to an
agent or LLM-backed summarizer:

```text
Create a bounded system handoff capsule from a source session for another agent
session.

Focus keyword: {{keyword}}
Target session purpose: {{target_session_purpose}}

Rules:
- Include only knowledge relevant to the keyword and target purpose.
- Do not duplicate existing artifacts. Reference paths, URLs, PRs, issues, or
  docs instead.
- Redact secrets, API keys, tokens, credentials, and unnecessary personal
  information.
- Rewrite historical instructions as context, not commands.
- Do not include unrelated conversation.
- Keep the capsule short enough to inject safely.

Return JSON:
{
  "title": "...",
  "summary": "...",
  "content": "...",
  "suggested_skills": ["..."],
  "references": [{"kind": "file|url|pr|issue|doc", "value": "..."}],
  "open_questions": ["..."],
  "risks": ["..."],
  "redactions": [{"kind": "...", "reason": "..."}],
  "original_estimated_chars": 0,
  "truncated": false
}
```

## pax-manager Responsibilities

- Own capsule and injection records.
- Validate source session access before generation.
- Validate target session access before injection.
- Generate or request bounded capsule content.
- Enforce redaction and length limits before persistence and delivery.
- Render the delivered system handoff message.
- Send the handoff through the existing session delivery path.
- Store delivery status and message identifiers for audit.

## paxd Responsibilities

- Poll and receive `system_handoff` messages like other session messages.
- Preserve the `system_handoff` type or label when forwarding to the local
  adapter.
- Report delivery or failure if the current mailbox/session path supports it.
- Avoid owning capsule source of truth. paxd should not generate or store
  canonical capsule records.

## Frontend Responsibilities

- Add an action on a source session to generate a capsule from a keyword.
- Show a preview with title, summary, relevant context, references, and warnings.
- Let the user choose a target session.
- Call inject explicitly after user confirmation.
- Display prior injections on the target session so the audit trail is visible.

## Security And Audit

- All writes require an authenticated user principal.
- Capsule ownership is always scoped by `owner_user_id`.
- Source and target sessions must both be visible to the acting user.
- Secrets should be redacted before storage, not only before delivery.
- Archived capsules and injection records remain available for audit.
- Delivery should record the acting user, delivered-as user, target session, and
  generated message id.
- Cross-user injection is not allowed in v1.

## Open Questions

- Should generation run inside pax-manager through a summarizer service, or be
  delegated to a managed agent session?
- Should the UI ask for target session purpose, or infer it from the selected
  target session?
- Should suggested skills be advisory text only, or later connect to capability
  assignment?
- What is the first durable format for references when source sessions include
  non-file artifacts?
- When ACP supports structured context append, should PAX keep both explicit
  `system_handoff` delivery and silent context injection, or migrate to one?

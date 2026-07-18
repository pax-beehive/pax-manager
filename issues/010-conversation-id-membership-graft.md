# ISSUE-010 Caller-supplied conversation_id joins any existing conversation (cross-tenant)

**Status:** resolved
**Severity:** blocker
**Component:** storage / auth
**Found:** 2026-07-18
**Resolved:** 2026-07-18

## Summary

`StartAgentConversation` trusts a caller-supplied `conversation_id`, returns
the existing conversation without any ownership check, and inserts the caller
as a member with role `owner`. Since `ListConversationMessages` authorizes
purely on `conversation_members` rows, this grants full read/write access to
other users' conversations.

## Round-trip affected

- [ ] User login / API key
- [ ] Agent registration / connection
- [x] User-agent messaging round-trip

## Current behavior

- `internal/manager/storage/agent_conversation.go:254` -
  `conversationID := strings.TrimSpace(req.ConversationID)` is used verbatim.
- `upsertAgentConversation` (`agent_conversation.go:1016`) does
  `ON CONFLICT (conversation_id) DO UPDATE ...` and returns the existing row
  with no owner/membership verification.
- `upsertConversationMember` (`agent_conversation.go:1037`) unconditionally
  inserts the caller's owner as role `owner` (and clears `left_at`).
- The only interaction gate, `agentConversationUsersCanInteract`
  (`agent_conversation.go:249`), checks source/target agent owners and is
  trivially satisfied by targeting one's own representative agent.
- Read side: `ListConversationMessages`
  (`internal/manager/storage/message_history.go:194`) only requires a
  `conversation_members` row - which the caller just created.

Entry points:

- `POST /api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/inquiries`
  (`internal/manager/agent_conversation_handlers.go:322`, body carries
  `conversation_id`).
- `POST /api/v1/node/agents/:agent_id/conversations` (NodeAuth).

Conversation IDs are unguessable but leak by design via
`AgentSession.ConversationID`, readable by team members on team-shared
agents. The memory store mirrors the same flaw
(`agent_conversation.go` memory variant).

## Expected behavior

When `conversation_id` names an existing conversation, the caller's owner
must already be an active member of it; otherwise the request is rejected
(not found). A fresh random ID is generated when none is supplied (current
behavior, keep).

## Impact

Any authenticated user (or node) who learns a victim conversation ID joins it
as `owner`, reads the full cross-user transcript, and injects further
turns/sessions into it.

## Affected code

```
internal/manager/storage/agent_conversation.go:232-300 - StartAgentConversation
internal/manager/storage/agent_conversation.go:1016-1052 - upsert helpers
internal/manager/storage/agent_conversation.go (memory) - same flow
internal/manager/storage/message_history.go:181-230 - read gate
internal/manager/agent_conversation_handlers.go:322-341 - user inquiry entry
```

## Proposed fix

In both stores, when `req.ConversationID` is provided, verify the source
owner already has an active membership row before upserting anything; return
`ErrNotFound` otherwise. Cover the user-inquiry and node-conversation entries
with regression tests.

## Resolution

`StartAgentConversation` in both stores now requires the source owner to
already hold an active membership when `conversation_id` names an existing
conversation, and returns not found otherwise (no membership row is created
on rejection). Fresh random IDs still work, and members can continue existing
threads. Regression tests cover the foreign-ID rejection and the member
continuation in `internal/manager/storage/memory_lifecycle_test.go`; the
scripted postgres conversation test gained the membership-check row.

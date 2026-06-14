# ISSUE-002 Session auto-creation on first message

**Status:** resolved
**Severity:** blocker
**Component:** session / mailbox
**Found:** 2026-06-13
**Resolved:** 2026-06-14

## Summary

When a user sends the first message to a new session (`POST /api/user/agents/:agentId/sessions/:sessionId/messages`), the `sessionTarget()` validation in `userapi.CreateMailboxMessage` checks that the session exists and belongs to the agent. For a brand-new session that hasn't been reported by paxd yet, this lookup fails with `ErrNotFound` and the message is rejected.

There is no explicit "create session" endpoint, so there is no way to establish a new session before sending the first message.

## Round-trip affected

- [x] User-agent messaging round-trip (user -> agent direction, first message)

## Current behavior

`userapi/service.go:214-218`:
```go
if req.SessionID != "" {
    if _, err := s.sessionTarget(c, principal, req.AgentID, req.SessionID); err != nil {
        return 0, nil, err
    }
}
```

`sessionTarget` (line 241-255) calls `GetSession` which queries `agent_sessions` table. A session that hasn't been upserted by a paxd status report will not exist -> 404.

Paxd only creates sessions when it reports status (`POST /api/agent/status` -> `UpsertAgentStatus`). This happens every 10s in the collector loop. But the first message arrives before the first status report.

## Expected behavior

Option A: Remove the strict session existence check for message creation. Allow messages to reference any session ID; the session will be created lazily when paxd first reports it.

Option B: Add an explicit `POST /api/user/agents/:agentId/sessions` endpoint to pre-create sessions before messaging.

## Impact

**Blocks the very first message in any new session.** The user cannot initiate a conversation with an agent.

## Affected code

```
internal/manager/userapi/service.go:214-218 - sessionTarget check on CreateMailboxMessage
internal/manager/userapi/service.go:241-255 - sessionTarget implementation
```

## Proposed fix

Option A (simpler): Skip `sessionTarget` check in `CreateMailboxMessage` when the session doesn't exist yet. The session row will be populated when paxd first reports it via `UpsertAgentStatus`. The message in mailbox is still tied to agent_id + session_id and will be pulled correctly.

## Resolution

The v1 user API now has explicit node-scoped session creation through
`POST /api/v1/user/:user_id/nodes/:node_id/agents/:agent_id/sessions`.
Message creation is scoped through user, node, agent, and session, and the v1
path supports the first-message flow without waiting for a paxd status report.

The legacy `/api/user/agents/:agent_id/sessions/:session_id/messages` route
keeps its strict compatibility behavior.

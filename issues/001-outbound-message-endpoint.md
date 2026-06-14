# ISSUE-001 Agent-to-user outbound message endpoint missing

**Status:** open  
**Severity:** blocker  
**Component:** api / mailbox  
**Found:** 2026-06-13  
**Resolved:** —

## Summary

pax-manager has no endpoint for an agent to push a completed turn result (events, file changes, token usage) back to the cloud. The agent-facing API only supports `POST /api/agent/messages/:messageId/result` which accepts a status + error string — not structured turn data. This means agents cannot send responses to users through pax-manager.

## Round-trip affected

- [x] User-agent messaging round-trip (agent → user direction)

## Current behavior

paxd's cloud client calls `POST /api/agent/messages/outbound` to push a structured `OutboundMessage`:

```json
{
  "agent_id": "...",
  "session_id": "...",
  "type": "turn_result",
  "turn_id": "...",
  "response_id": "...",
  "status": "completed",
  "events": [...],
  "file_changes": [...]
}
```

This endpoint does not exist in pax-manager. The `internal/transport/http/router/paxmanager/api/pax_manager.go` generated routes are:

```
POST /api/agent/register
POST /api/agent/status
GET  /api/agent/mailbox
GET  /api/agent/sessions/:sessionId/mailbox
POST /api/agent/messages/offset
POST /api/agent/messages/:messageId/result
```

No outbound/response creation endpoint.

Also missing: `POST /api/agent/messages/:messageId/delivered` for delivery confirmation that paxd's `MarkDelivered` calls.

## Expected behavior

An endpoint that allows authenticated agents to:
1. Mark a mailbox message as delivered (`POST .../delivered`)
2. Push a turn result with structured events back (`POST /api/agent/messages/outbound` or similar)

The result should be stored as a new mailbox row (direction: agent→user) or written into the existing message's result field.

## Impact

**Blocker for the full messaging round-trip.** User can send messages to agents, but agents cannot respond. The entire product loop is broken.

## Affected code

```
internal/transport/http/router/paxmanager/api/pax_manager.go — no outbound route
internal/transport/http/handler/handler.go:17-46 — Service interface, no outbound method
internal/manager/paxd_facing.go — no handle method for outbound
internal/manager/paxd/service.go — no CreateOutbound/ReportDelivery in Store interface
internal/manager/storage/postgres_store.go — no UPSERT for outbound messages
```

## Proposed fix

Add `POST /api/agent/messages/outbound` and `POST /api/agent/messages/:messageId/delivered` to the Thrift IDL, regenerate, implement in store and service layers. Outbound creates a new mailbox message with session_id linking.

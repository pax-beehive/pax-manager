# ISSUE-003 Missing mailbox "delivered" status transition

**Status:** resolved
**Severity:** high
**Component:** mailbox
**Found:** 2026-06-13
**Resolved:** 2026-06-14

## Summary

The mailbox message lifecycle only has two states: `pending` (created by user) and `completed`/`failed` (set by `POST /api/agent/messages/:messageId/result`). There is no intermediate `delivered` state to confirm the agent has received and acknowledged the message.

Additionally, paxd calls `POST /api/agent/messages/:messageId/delivered` (via `cloud.Client.MarkDelivered`) but this endpoint does not exist on pax-manager.

## Round-trip affected

- [x] User-agent messaging round-trip

## Current behavior

- Message created -> `status = 'pending'`
- Agent pulls message -> **no status change** (message remains `pending`)
- Agent reports result -> `status = 'completed'` or `status = ...` (set by `ReportMessageResult`)

The paxd `cloud.Client.MarkDelivered()` call fails silently (404) since the endpoint doesn't exist.

The paxd `executor` calls `MarkDelivered` after fetching messages, and then calls `ReportCompleted` after execution. Neither works against the current pax-manager API.

## Expected behavior

Three-state lifecycle:
1. `pending` - created by user, not yet seen by agent
2. `delivered` - agent has pulled and acknowledged (atomically, with timestamp)
3. `completed` / `failed` - agent has processed, result attached

This enables the dashboard to show message delivery status.

## Impact

- Dashboard cannot distinguish "sent but agent hasn't seen it" from "agent has it but hasn't responded yet"
- Orphan detection relies on timeout alone, no delivery tracking

## Affected code

```
internal/manager/paxd/service.go - Store interface, no MarkDelivered
internal/manager/storage/postgres_store.go - no MarkDelivered implementation
internal/transport/http/router/paxmanager/api/pax_manager.go - no /delivered route
db/init.sql - mailbox table has delivered_at column but no endpoint sets it
```

## Proposed fix

Add `POST /api/agent/messages/:messageId/delivered` to the Thrift IDL. Implement `MarkDelivered` on the store that sets `status = 'delivered'` and `delivered_at = NOW()`. Pull queries should filter `WHERE status IN ('pending', 'delivered')` or just `'pending'` depending on desired redelivery behavior.

## Resolution

The v1 node API now exposes
`POST /api/v1/node/messages/:message_id/delivered`. Mailbox pulls also mark
pending messages delivered atomically with `delivered_at`, so the dashboard can
distinguish pending, delivered, completed, and failed states.

The legacy `/api/agent/messages/:message_id/delivered` route was not added.
Paxd should migrate to the v1 node API for this behavior.

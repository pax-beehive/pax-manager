# ISSUE-015 ACP request ID reuse corrupts long session history

**Status:** resolved
**Severity:** high
**Component:** session / storage
**Found:** 2026-07-22
**Resolved:** 2026-07-22

## Summary

Manager-generated ACP request IDs were allocated by an in-memory per-agent
counter. After pax-manager restarted, the counter could reuse an earlier ID in
the same durable session. User prompt history derives its logical key and
message ID from that request ID, so the new prompt overwrote an older row.

## Round-trip affected

- [x] User-agent messaging round-trip

## Current behavior

Long sessions that survive a manager restart can lose prompt rows. The updated
prompt keeps the old database row ID and creation time, so the latest history
page may omit it entirely.

## Expected behavior

Manager-generated ACP request IDs remain unique for an agent across process
restarts and concurrent manager instances.

## Impact

Durable prompt history can become incomplete, and request/response grouping can
appear incorrect once the overwritten row falls outside the latest history
page.

## Affected code

```
internal/manager/acp_tunnel.go - in-memory manager request sequence
internal/manager/conversation.go - manager-generated ACP requests
internal/manager/acp_history.go - prompt history logical key
```

## Resolution

The agent row now owns a durable `next_acp_request_id` counter. PostgreSQL
allocates IDs with an atomic `UPDATE ... RETURNING`, while MemoryStore performs
the same increment under its lock. Existing agents are backfilled to at least
the maximum numeric request ID found in their prompt history before new IDs are
allocated.

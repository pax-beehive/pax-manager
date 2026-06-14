# ISSUE-007 Mailbox pull response envelope wrapping

**Status:** open
**Severity:** high
**Component:** api
**Found:** 2026-06-13
**Resolved:** -

## Summary

`GET /api/agent/mailbox` returns mailbox messages wrapped in the standard `{data, code, message}` envelope. paxd's `cloud.Client.FetchMessages` decodes the response body directly as `[]Message` - it does not unwrap the envelope. This means paxd polling through HTTP will receive a `{data: {...}}` wrapper it cannot parse.

The WebSocket path avoids this because the WS protocol already has its own envelope. But the HTTP fallback path is broken.

## Round-trip affected

- [x] Agent registration / connection (HTTP fallback)
- [x] User-agent messaging round-trip (message delivery to agent)

## Current behavior

pax-manager response for `GET /api/agent/mailbox?offset=0&limit=10`:
```json
{
  "data": {"messages": [...], "max_offset": 42, "has_more": true},
  "code": 200,
  "message": "ok"
}
```

paxd `cloud.Client.FetchMessages` (client.go:162-180):
```go
var msgs []Message
json.NewDecoder(resp.Body).Decode(&msgs)  // expects top-level array!
```

paxd expects a flat `[]Message` array, but receives `{data: {messages: [...]}}`.

Similarly, the WebSocket path uses `paxd.PullMailbox` -> `store.PullMailbox` which returns `MailboxPull{Messages, MaxOffset, HasMore}`. This is returned inside the WS `agentWSResponse{Data: pull}` which is the correct WS protocol. But the HTTP REST path wraps it again in the `apiResponse` envelope.

## Expected behavior

Agent-facing REST endpoints should either:
A. Not use the `{data, code, message}` envelope (flat response)
B. Use a consistent envelope that paxd knows how to parse

Since the WebSocket protocol already has its own envelope (`{type, request_id, data, code, message}`), the HTTP REST path should probably match. But the current behavior mixes two envelope layers.

## Impact

**HTTP polling fallback is broken.** If WebSocket connection fails and paxd falls back to HTTP `FetchMessages`, it cannot parse responses. WebSocket path works correctly.

## Affected code

```
internal/manager/server.go:179-181 - writeData wraps in apiResponse
internal/manager/paxd/service.go:128-141 - PullMailbox returns (int, any, error), the 'any' gets wrapped
```

## Proposed fix

For agent-facing endpoints (register, status, mailbox, offset, message/result), skip the `apiResponse` envelope and return data directly. The WebSocket already has its own framing - the REST path should match. Or, add unwrapping logic in paxd's HTTP cloud client.

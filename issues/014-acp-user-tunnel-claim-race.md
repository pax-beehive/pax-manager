# ISSUE-014 ACP user tunnel claim races agent tunnel registration

**Status:** resolved
**Severity:** medium
**Component:** session / api
**Found:** 2026-07-18
**Resolved:** 2026-07-18

## Summary

`handleUserACPTunnel` claimed the agent tunnel with a single non-retrying
`claimAny` call, but the agent tunnel only registers itself in the hub after
its transport recovery finishes. A user tunnel connecting in that window got
404 "agent tunnel not connected" and the websocket handshake failed.

## Round-trip affected

- [x] User-agent messaging round-trip

## Current behavior

- The agent ACP tunnel logs "connected; transport recovering" at upgrade time
  and only calls `hub.add` once the transport is ready
  (`internal/manager/acp_tunnel.go`, `forwardAgentFrames` callback).
- The user tunnel claimed immediately with no wait, so
  `TestACPTunnelRuntimeStateIntegration` failed with
  `websocket: bad handshake` when the claim landed in the registration
  window. The same race existed at base commit `5821659`.

## Expected behavior

The user tunnel claim waits briefly for the agent tunnel to register before
giving up.

## Impact

Flaky/failing ACP runtime state integration test; real clients connecting a
user tunnel immediately after the agent tunnel could see spurious 404s.

## Affected code

```
internal/manager/acp_tunnel.go:1269 - user tunnel claim site
internal/manager/acp_tunnel.go:487 - existing claimAnyWait helper (previously test-only)
```

## Proposed fix

Use the existing `claimAnyWait` helper (bounded wait + interval) at the user
tunnel claim site.

## Resolution

`handleUserACPTunnel` now claims via `claimAnyWait` with a 5s deadline and
50ms interval, covering the agent tunnel registration window.

## Follow-up

The integration test's agent tunnel client also predated the reliablemq
reconcile protocol: it never sent the required `reconcile_request` envelope,
so the server never registered the tunnel in the hub at all (registration
only happens after reconciliation completes). `connectACPAgentTunnel` in
`integration/acp_runtime_state_integration_test.go` now performs the
reconcile handshake (request + response validation) after dialing, matching
the production protocol.

# 042: OpenClaw conversation creation fails on per-session MCP injection

- Status: resolved
- Resolved: 2026-10-19
- Severity: high
- Component: session / ACP

## Problem

The conversation endpoint unconditionally injects the Pax conversation MCP
server into ACP session setup. OpenClaw 2026.10.4 rejects all per-session MCP
servers in its Gateway-backed ACP bridge, returning an RPC internal error and
causing the Manager conversation endpoint to return HTTP 502.

The observed failure occurred after the Gateway and both ACP slots were ready.
Starting the Gateway only resolves the earlier connection-refused failure.

## Resolution

Return an empty MCP server array for OpenClaw session creation and resumption,
including agent conversation delivery. Preserve the agent type when creating
console sessions so later route recovery applies the same compatibility rule.
Other agent types retain the existing Pax conversation MCP injection.

An HTTP/WebSocket regression test checks the actual session/new frame, successful
session initialization, and persistence of the OpenClaw agent type. Existing
conversation streaming and MCP tests cover agents that support injection.

This fixes the current structured Manager conversation endpoint. Direct ACP
clients remain responsible for their own session setup parameters. OpenClaw
still requires a running Gateway and configured model credentials. Its Pax
conversation MCP tools are unavailable through per-session injection; enabling
them through Gateway configuration requires a separate integration.

Deployment of the Manager change is required for existing hosted environments.

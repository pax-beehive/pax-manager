# [ISSUE-070] Pax MCP automatic approval does not verify the effective registration origin

**Status:** open
**Severity:** high
**Component:** ACP / MCP / auth
**Found:** 2026-11-05
**Resolved:** -

## Summary

Automatic approval matches the expected per-session Pax server name and the
identity reported by the authenticated ACP host, but does not independently
verify which effective MCP registration or connection implements that name.

## Current behavior

The Manager checks the owner, node, agent, session, active turn, registered tool
name list and expected session-derived server name. Codex calls also require
matching top-level server/tool fields. These checks remain enabled.

The suffix is a public session-derived identifier, not proof of process origin.
Source review found adapter-specific cases outside the current checks:

- Hermes joins normalized server and tool names using single underscores. A
  server named pax and a tool named conversation_<suffix>_workspace_read can
  produce the same flattened name as the intended Pax tool. Its MCP registry
  also permits replacement between MCP tools.
- Codex and Hermes can retain an already configured same-name native server
  instead of applying the ACP-injected configuration.
- Paxd rewrites injected Pax commands to its own executable, but the Manager
  does not verify that this was the effective registration used by the host.

These are source-level findings; no live malicious MCP or exploit was run.
Current unit, integration and race checks validate the implemented matching and
execution behavior, not this missing origin guarantee.

## Decision and priority

On 2026-11-05 the user explicitly chose to keep automatic approval working and
defer configuration-injection and origin-binding hardening. This issue is a
non-blocking follow-up for PR #224 and is not a security fix completed by it.

Keep the current automatic approval policy for qualifying Pax calls. Do not
introduce per-call manual approval or an approval page as a workaround for this
issue. Other tools retain their existing approval settings. Existing job,
capability and data-authorization checks remain enforced; they do not prove the
identity or safety of an arbitrary local MCP implementation.

## Expected behavior

The trusted host/adapter should resolve the actual registered MCP connection or
instance and associate each call with it. Same-name registrations and
normalization collisions should be rejected or disambiguated. A displayed tool
name, readOnlyHint or session suffix alone must not establish origin.

The authenticated node and ACP runtime remain explicit trust boundaries.

## Affected code

- internal/manager/acp_owned_mcp.go
- internal/manager/agent_conversation_handlers.go
- Paxd ACP MCP injection and native adapter registration/namespace handling

## Follow-up acceptance criteria

- Bind approval policy to the effective trusted registration and connection.
- Add same-name native configuration and Hermes normalization-collision cases.
- Verify the actual adapter registration precedence for supported versions.
- Preserve unattended Secretary execution while adding origin verification.

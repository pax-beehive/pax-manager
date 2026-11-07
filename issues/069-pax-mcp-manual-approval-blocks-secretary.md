# ISSUE-069 Pax MCP calls interrupt manual Secretary executions

**Status:** resolved
**Severity:** high
**Component:** ACP / MCP / Secretary
**Found:** 2026-11-05
**Resolved:** 2026-11-05

## Problem

Secretary sessions use manual approval. DSCODE requests permission for the
read-only workspace_summaries tool, even though the MCP declaration already has
readOnlyHint=true. The generic conversation reader interrupts on this request;
Secretary returns an unknown outcome and revokes its execution capability while
the native prompt remains waiting for approval. Subsequent executions can fail
because the ACP slot still has an active prompt.

## Resolution

Pax-owned tools now have an explicit automatic approval policy in the ACP
middleware. It matches the expected server name from the Manager injection request and
known Pax tools using the ACP host-reported identity, supports different adapter representations, correlates ID-only
requests with the active turn, and records an audited one-time decision. The
pending permission event is consumed rather than interrupting a manual stream.
Other tools retain their existing approval policy; data authorization is
unchanged. See the [handoff](../docs/handoff_20261105_192000.md).

The effective MCP connection origin is not independently verified. The user
chose to defer that hardening without blocking automatic approval; the open
follow-up is [issue 070](070-pax-mcp-origin-binding.md). No manual-approval page
is introduced for that follow-up.

The deployed change was validated with fresh Secretary executions: both
workspace_summaries and workspace_read received audited allow_once decisions
under pax_owned_mcp, their responses reached the agent, and summaries committed.
Previously blocked native prompts were terminated before scoped task retries.
The code does not automatically migrate or retry those old executions. An
execution whose capability was revoked must not resume with its old authority.

Further investigation found separate source-completion waiting and native
terminal-tool problems. These are tracked independently; they do not negate the
observed MCP approval decisions.

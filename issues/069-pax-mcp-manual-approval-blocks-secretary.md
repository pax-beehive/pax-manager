# ISSUE-069 Pax MCP calls interrupt manual Secretary executions

**Status:** partial
**Severity:** high
**Component:** ACP / MCP / Secretary
**Found:** 2026-11-05
**Resolved:** -

## Problem

Secretary sessions use manual approval. DSCODE requests permission for the
read-only workspace_summaries tool, even though the MCP declaration already has
readOnlyHint=true. The generic conversation reader interrupts on this request;
Secretary returns an unknown outcome and revokes its execution capability while
the native prompt remains waiting for approval. Subsequent executions can fail
because the ACP slot still has an active prompt.

## Current note

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

This issue remains partial until deployment and real Secretary validation.
Previously blocked native prompts and old unknown batches are not migrated or
retried by this code change. An existing execution whose capability was revoked
must not be resumed as though its original read authorization were still valid.

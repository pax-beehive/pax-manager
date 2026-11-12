# [ISSUE-084] Native harness plan usage is not visible to node owners

**Status:** partial
**Severity:** medium
**Component:** api / node control
**Found:** 2026-11-12
**Resolved:** -

## Summary

Node owners can see whether a native harness is signed in and which account it
uses, but not how much of the subscription plan limits that account has used.

## Current behavior

Before this change the current node user API exposed only `harness_auth.status`
and `harness_auth.login`. Plan usage (Claude 5-hour and weekly windows, Codex
ChatGPT rate limits) was visible only inside the native CLIs on the node.

## Expected behavior

The owner can read sanitized plan usage for a node harness on demand, without
paxd or Manager reading native credential files, and without Manager persisting
or caching the result.

## Current note

The current node user API now exposes
`GET /api/v1/user/:user_id/nodes/:node_id/harnesses/:harness/usage`, which
forwards a `harness_usage.status` control query and returns a sanitized
`harness_usage` view (`available`, `unsupported`, or `unavailable` with an
allowlisted `error_code`). A paxd that predates the query is reported as
`unavailable` with `daemon_unsupported`. The legacy agent API is unchanged.

The paxd reader (native CLI query, caching) and the Console display ship in
companion paxd and pax-console pull requests. Mark this issue resolved once
those are merged.

## Follow-up

One subscription is often signed in on several nodes, and each node reports its
own view of the same account. A later change may aggregate usage per
subscription account across nodes and add API key credit balances. That is out
of scope here; the current API stays per node and per harness.

## Affected code

```
api/pax_manager.thrift - GetNodeHarnessUsage and HarnessUsage* structs
internal/manager/userapi/harness_usage.go - ownership, query, sanitization
internal/manager/node_harness_usage.go - HTTP binding with no-store
```

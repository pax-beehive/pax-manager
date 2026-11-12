# [ISSUE-083] Default workspace search scope stops at one agent

**Status:** resolved
**Severity:** medium
**Component:** auth / search / storage / MCP
**Found:** 2026-11-12
**Resolved:** 2026-11-12

## Summary

Agents sharing one node cannot search each other's sessions without requesting
an expansion. The search contract lacks an explicit node dimension.

## Resolution

Ordinary node workspace searches now declare node, agent and session ranges.
The default covers the owner's sessions on the authenticated calling node.
Cross-node requests still require a matching turn-scoped approval. Expansion
requests and prompt preauthorization support node_ids as well. Existing request
status, cancellation and deadlines are unchanged.

Source node attribution is persisted in search projections and enforced in ES
and fresh source checks. Ambiguous historical sources are excluded from narrow
node scopes. Legacy omitted node dimensions retain their all-node meaning;
ordinary searches using that shape require expansion approval. This applies to
the current node API; Secretary keeps its separate capability contract.

Deployment and index backfill remain operational steps. See the node scope
handoff for rollout requirements. No live service was modified.

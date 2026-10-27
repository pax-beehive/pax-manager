# 044: Completed transport journal history grows without bound

- Status: resolved
- Resolved: 2026-10-26
- Severity: high
- Component: storage / transport

## Problem

Manager exposed a completed-frame deletion method without scheduling it. Completed
journal history accumulated indefinitely, increasing storage and startup backfill
costs. The existing deletion method did not preserve a diagnostic tail or explicitly
check persistent replay watermarks.

## Resolution

Add a bounded background PostgreSQL cleanup worker, enabled by default. Preserve
72 hours of recent history, a 100-position tail, unfinished frames, missing queue
state, and frames beyond persisted safe ACK/applied boundaries. Cleanup skips
locked rows and retains all queue state. No production data was deleted during
implementation. Applies to the shared journal for both node and legacy agent APIs.

See `docs/transport-journal-retention.md` for configuration and rollout limitations.

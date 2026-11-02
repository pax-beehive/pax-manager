# Raw-event limits fragment Secretary model batches

- Status: resolved
- Severity: medium
- Component: storage / Secretary batching
- Resolved: 2026-11-02

## Problem

The PostgreSQL loader takes 64 dirty message rows before excluding reasoning,
tools, replacement records, and unchanged canonical text. The model receives
only a few visible messages but pays for another prompt and rolling baseline.
Across 284 production executions, canonical message count had median six,
P90 fifteen, and maximum thirty-six; 228 executions contained at most ten.

## Resolution

The tested follow-up pages raw changes by revision/message ID, at 128 rows per
page and 1,024 scanned rows per claim. Only canonical candidates load full
messages. It fills the existing 64-message/128 KiB limits and bounds removals,
while skipped events still advance the checkpoint. Invisible-only scan windows
are acknowledged without invoking the model. MemoryStore uses the same rules.

The PR includes issue 057's recovery changes through its base branch and can be
merged into that branch before PR 213 reaches main. Changing batch contents or
the compact input policy changes RunID, so some pending old model outputs will
be replaced by new executions. A settings disable/re-enable independently
changes that identity. This is an execution-cost consideration, not a reason to
weaken source validation or settings fencing. Identical retries within the new
policy retain their RunID. Genuine invalid evidence remains a separate decision.

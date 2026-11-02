# Raw-event limits fragment Secretary model batches

- Status: partial
- Severity: medium
- Component: storage / Secretary batching
- Resolved: -

## Problem

The PostgreSQL loader takes 64 dirty message rows before excluding reasoning,
tools, replacement records, and unchanged canonical text. The model receives
only a few visible messages but pays for another prompt and rolling baseline.
Across 284 production executions, canonical message count had median six,
P90 fifteen, and maximum thirty-six; 228 executions contained at most ten.

## Current note

The tested follow-up pages raw changes by revision/message ID, at 128 rows per
page and 1,024 scanned rows per claim. Only canonical candidates load full
messages. It fills the existing 64-message/128 KiB limits and bounds removals,
while skipped events still advance the checkpoint. Invisible-only scan windows
are acknowledged without invoking the model. MemoryStore uses the same rules.

The rollout is intentionally separate from issue 057: changing batch contents
changes RunID, so valid already-generated failed responses must first recover
under the old batch boundaries. This branch remains a draft until that recovery
has been deployed and checked. Identical retries within the new batch planner
retain their RunID. Genuine invalid evidence remains a separate retry decision.

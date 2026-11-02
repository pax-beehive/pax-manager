# Automatic Secretary bypasses compact input views

- Status: resolved
- Severity: high
- Component: Secretary / prompt / source reads
- Resolved: 2026-11-02

## Problem

The automatic prompt directly sends each changed canonical message's full body.
The earlier experiment used full user messages and an assistant last view, with
first/full expansion available on demand. Raw-event filtering and batch filling
do not implement that compact-view requirement. New evidence binding must also
respect the text actually exposed in the default prompt or verified tool reads.

## Resolution

The PR adds optional exact-turn reads using workspace_history with session_id,
turn_id, and view, and returns real turn metadata. Existing bounded pagination,
source versions, receipts, owner/session checks, and tool/reasoning exclusion
remain in force. Missing turn IDs are not inferred from neighboring sequences.

Automatic input now uses full user messages and the existing assistant last
view: the last natural paragraph of each canonical assistant message. This
follows the prototype/read API meaning, stated explicitly to the user; it is
not the final complete assistant reply of a turn or a streaming message part.

Full source hashes/snapshots are preserved. Claims bind only to exposed prompt
text or exact verified expanded reads; own-delta receipts survive recovery of
completed outputs through re-verification. The 128 KiB model text budget and
8 MiB canonical-source budget are checked separately, and oversized serialized
source loads are rejected before full transfer. Input policy participates in
RunID so old full-prompt output is never treated as a compact-view execution.
No production deployment or model execution was performed for this change.

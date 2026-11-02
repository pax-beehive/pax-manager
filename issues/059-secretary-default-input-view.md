# Automatic Secretary bypasses compact input views

- Status: partial
- Severity: high
- Component: Secretary / prompt / source reads
- Resolved: -

## Problem

The automatic prompt directly sends each changed canonical message's full body.
The earlier experiment used full user messages and an assistant last view, with
first/full expansion available on demand. Raw-event filtering and batch filling
do not implement that compact-view requirement. New evidence binding must also
respect the text actually exposed in the default prompt or verified tool reads.

## Current note

The draft adds optional exact-turn reads using workspace_history with session_id,
turn_id, and view, and returns real turn metadata. Existing bounded pagination,
source versions, receipts, owner/session checks, and tool/reasoning exclusion
remain in force. Missing turn IDs are not inferred from neighboring sequences.

The automated prompt correction is held separately pending one semantic detail:
the prototype's last view selects a message's last paragraph, while a turn's final
assistant reply can contain multiple paragraphs and multiple earlier reply
segments. The user has been asked which unit the default input should retain.
No new automatic default is published until that is resolved.

The local candidate preserves full source hashes/snapshots, binds quotes only to
exposed prompt text or signed expanded reads, budgets the sent view separately
from full source data, and versions the execution input policy. It has not been
deployed or used to start model work.

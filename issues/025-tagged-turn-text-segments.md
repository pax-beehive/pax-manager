# [ISSUE-025] Tagged turns collapse distinct text segments

**Status:** resolved
**Severity:** high
**Component:** history / console
**Found:** 2026-09-16
**Resolved:** 2026-09-16

## Cause

acpHistoryLogicalKey prioritized turn_id over historyGroupID. For tagged turns,
text A, a tool, and text B produced one AB row and a tool row, despite the
history group correctly changing at the tool boundary. Real-time rendering
uses contiguous frame groups and could still show A/tool/B, hiding the mismatch.

## Resolution

Include both turn identity and text group identity in the durable text key.
Without an available group, use the transport sequence instead of merging all
text of the turn. Existing terminal/tool identity rules are unchanged.
New tagged text rows carry raw_json.text_layout=segment. Console preserves
these row boundaries and their sequence order instead of moving them to the
end or joining adjacent page-context rows. Untagged legacy grouping is retained.

Old whole-turn aggregates cannot be accurately split from their concatenated
text alone. No existing records are rewritten or guessed apart by this fix.

## Verification

The real projection path, using both immediate and production batched text
sinks, covers consecutive chunks, tool/terminal boundaries, and cross-turn
isolation. Console tests cover completed history, omitted tool pages, mixed
old/new rows during rollout, and legacy aggregate compatibility.

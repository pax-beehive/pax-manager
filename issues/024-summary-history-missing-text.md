# [ISSUE-024] Summary history loses prompts and long-turn replies

**Status:** resolved
**Severity:** high
**Component:** history / console
**Found:** 2026-09-16
**Resolved:** 2026-09-16

## Cause

The summary service excluded extracted prompt parts, assuming Console would
read text from the preserved session/prompt frame. Console did not implement
that fallback. Its attachment handling could therefore show a prompt without
text, or no prompt at all.

The new 100-row page counted tool events. Assistant aggregates keep the sequence
of their first chunk, so both prompt and final answer could fall outside the
latest page in a long turn. The visible page then contained tools and turn_done.

## Resolution

Keep canonical prompt text parts in summary responses. Supplement each summary
page with non-tool context for the turns represented on its base page, scoped
to the same agent/session and bounded by the page head. Preserve the base page
cursors so intermediate tool pages remain reachable. Tool details remain lazy.

Console also reads text from a raw prompt frame when text parts are absent and
deduplicates overlapping page context by message_id before sequence ordering.
No persisted messages are rewritten. No production data was inspected for this
fix; the defects were reproduced against the merged code and synthetic history.

## Verification

Regression cases cover raw-only prompts, canonical text precedence, a completed
turn with 150 tools, first and older pages retaining both prompt and answer,
cursor preservation, scoped SQL and duplicate-free cross-page assembly.

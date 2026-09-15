# ACP transport loses the Manager turn identity

**Status:** resolved
**Severity:** high
**Component:** session / storage
**Found:** 2026-09-15
**Resolved:** 2026-09-15

## Problem

Manager generated a business turn ID while paxd independently generated a
runtime turn instance ID. Snapshot projection then used the second ID as
active_turn_id, disconnecting current runtime identity from message history.
Replayed output could also inherit a newer in-memory current turn.

## Resolution

Ordinary ACP envelopes now carry Manager turn_id in both directions. Conversation
and raw user prompt paths persist that identity in history. Raw prompts without
a business ID receive a new one instead of inheriting the preceding turn.
Paxd uses the envelope ID and echoes it in snapshots; legacy instance ID fields
are aliases of that same value. Conflicting snapshot aliases are rejected.
Tagged output from another turn remains eligible for history but cannot update
the current ACP runtime projection. Tool and terminal history merge keys include
the envelope turn ID so a new execution cannot overwrite old worker tool IDs.

This resolves the ordinary non-E2EE Manager/paxd ACP path. E2EE/local and older
Manager callers without turn metadata retain the previous fallback. No deployed
state or historical rows were rewritten. The separate database deadlock in
issue 019 is still open.

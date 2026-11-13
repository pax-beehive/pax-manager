# ISSUE-088 Secretary ignores a correlated native completion

**Status:** partial
**Severity:** high
**Component:** Secretary / ACP completion / result recovery
**Found:** 2026-11-13
**Resolved:** -

## Problem

One historical task had a completed native turn after about ten seconds. Both
the reliable journal and Manager history contained the successful end_turn RPC
response with the exact request, session and business turn. Secretary's result
reader recognized only the additional conversation turn_done marker. Without
that marker it reported unknown indefinitely despite the original result.

## Current note

The reader also recognizes a native completion only after verifying the
Manager-prepared dispatch, immutable attempt binding, owner/agent/session/turn,
ACP source/direction/status/role, exact RPC request ID, JSON-RPC version, absence
of an error and stopReason=end_turn. A bare message type or idle runtime is not
completion proof. The reader stops at that terminal, so later notifications do
not replace the completed answer. Known failure retains precedence.

Domain and actual reliable-frame regressions cover success without the extra
conversation marker and reject altered identity, binding, RPC IDs, errors and
incomplete results. Deployment and checking the original historical attempt
remain follow-ups; no terminal marker is fabricated and no prompt is resent by
this correction.

Late successful responses do not gain a new automatic queue wake in this change.
Existing blocked tasks can check the original attempt through the task recovery
action after deployment. Missing correlation remains unknown. The audited
historical answer is one complete JSON assistant row, so it does not require
cross-message output concatenation.

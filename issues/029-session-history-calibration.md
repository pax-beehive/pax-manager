# [ISSUE-029] Completed session history is not reliably reconciled after browser tab changes

**Status:** resolved
**Severity:** high
**Component:** session / storage / api
**Found:** 2026-09-17
**Resolved:** 2026-09-17

## Summary

The Console needs a cheap durable history head and bounded whole-turn reads to
repair missed messages and replace partial conversation output after completion.

## Resolution

Node/agent/session detail now includes latest_message_id, latest_message_seq,
and latest_turn_id. PostgreSQL selects only those fields from the latest scoped
message; no session write, schema change, or paxd report is added.

Session history accepts turn_id with view=summary and seq pagination. Turn pages
obey limit and do not expand non-tool context outside the page. General summary
history retains its existing context behavior. The canonical session wrapper
forwards both capabilities. Authorization is checked before either read.

The companion Console change polls detail, catches up from the second newest
known sequence, and atomically calibrates completed turns after reading all pages.
This fix covers the normal Manager transport; encrypted sessions retain their
existing history flow. Production deployment and real-browser acceptance remain
separate from local regression validation.

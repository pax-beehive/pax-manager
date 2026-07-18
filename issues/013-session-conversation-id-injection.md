# ISSUE-013 Session creation stamps caller-supplied conversation_id (cross-tenant write injection)

**Status:** resolved
**Severity:** medium
**Component:** session / storage
**Found:** 2026-07-18
**Resolved:** 2026-07-18

## Summary

`CreateNodeAgentSession` copies caller-supplied `conversation_id`,
`profile_id`, `representative_agent_id`, and `created_by_user_id` onto the
session without validating them. Subsequent session messages are stamped with
the victim conversation ID, polluting other users' conversation timelines.

## Round-trip affected

- [ ] User login / API key
- [ ] Agent registration / connection
- [x] User-agent messaging round-trip

## Current behavior

- The HTTP layer passes the raw body (including `conversation_id`,
  `profile_id`, `representative_agent_id`, `created_by_user_id`) through
  (`internal/manager/userapi/service.go:1422`).
- `internal/manager/storage/postgres_store.go:1579` -
  `UPDATE agent_sessions SET conversation_id = COALESCE(NULLIF($3,''), ...),
  created_by_user_id = ...` for the caller's own agent session with no
  membership/ownership validation of the referenced conversation.
- Message persistence derives `conversation_id` from the session
  (`internal/manager/storage/message_history.go:62` `messageConversationID`),
  so attacker sessions write into the victim conversation timeline as seen by
  `ListConversationMessages`.

Unlike ISSUE-010 this adds no membership row, so it is write-only pollution;
`created_by_user_id` is additionally spoofable.

## Expected behavior

- A caller-supplied `conversation_id` is only accepted when the principal is
  already an active member of that conversation (the internal A2A flow
  satisfies this because the source owner was just added as a member).
- `created_by_user_id` is forced to the principal's user ID.
- `profile_id` / `representative_agent_id` are validated as belonging to the
  principal or dropped from the user-facing request path.

## Impact

Cross-tenant write/injection into other users' conversation history and
spoofed authorship metadata on sessions. No read access is granted.

## Affected code

```
internal/manager/userapi/service.go:1422-1442 - raw body passthrough
internal/manager/storage/postgres_store.go:1579-1592 - unvalidated UPDATE
internal/manager/storage/memory_store.go - mirror of CreateNodeAgentSession
internal/manager/storage/message_history.go:62-75 - conversation stamping
```

## Proposed fix

In both stores, when `conversation_id` is provided, require an active
membership row for the principal before stamping; always set
`created_by_user_id = principal.User.UserID`; validate or ignore the
caller-supplied profile/representative IDs.

## Resolution

`CreateNodeAgentSession` in both stores now validates session reference
fields before stamping: `conversation_id` requires an active membership of
the principal, `profile_id` must be owned by the principal, and
`representative_agent_id` must resolve to an active representative whose
runtime agent the principal owns; violations return not found.
`created_by_user_id` is coerced to the principal unless it names another
active member of the same conversation (the internal A2A flow), and the
original creator is preserved on re-upsert. Regression tests cover the
foreign conversation/profile/representative rejections and the created-by
coercion in `internal/manager/storage/memory_lifecycle_test.go`; the
scripted postgres conversation test gained the validation query rows.

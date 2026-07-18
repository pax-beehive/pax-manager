# ISSUE-012 Representative agent upsert can overwrite another user's profile and impersonate owners

**Status:** resolved
**Severity:** medium
**Component:** storage / auth
**Found:** 2026-07-18
**Resolved:** 2026-07-18

## Summary

`UpsertRepresentativeAgent` only verifies ownership of `runtime_agent_id`.
The caller-controlled `profile_id` is upserted with
`ON CONFLICT (profile_id) DO UPDATE`, overwriting another user's agent
profile content, and `represents_type`/`represents_id` are accepted without
validating that the caller is the claimed user or a member of the claimed
team.

## Round-trip affected

- [ ] User login / API key
- [ ] Agent registration / connection
- [x] User-agent messaging round-trip

## Current behavior

- `internal/manager/storage/agent_conversation.go:162` - `req.ProfileID` is
  used verbatim; when omitted it defaults to a deterministic
  `sha256(agentID + ":" + ownerUserID)`-derived ID that team members can
  compute for a victim's agent (`agent_conversation.go:1505`).
- `upsertAgentProfile` (`agent_conversation.go:216`) overwrites
  display_name/description/card/metadata on conflict, regardless of the
  existing profile's `owner_id`.
- `agent_conversation.go:166` - `represents_id` defaults to the caller's own
  user ID but accepts any value; `agentOwnerSubject` later resolves it, so a
  malicious representative agent presents itself as representing another user
  or team.
- Entry point: `POST /api/v1/user/:user_id/representative-agents`
  (`internal/manager/agent_conversation_handlers.go:306`).

## Expected behavior

- A caller-supplied `profile_id` that already exists and is owned by a
  different user is rejected (conflict).
- `represents_type=user` requires `represents_id == agent owner`;
  `represents_type=team` requires the owner to be an active member of that
  team.

## Impact

Cross-tenant integrity damage: defacement/re-purposing of another user's
representative-agent profile (served by `GetAgentOwnerInfo` and stamped onto
conversation sessions), plus stored identity impersonation toward
conversation counterparties.

## Affected code

```
internal/manager/storage/agent_conversation.go:149-197 - UpsertRepresentativeAgent
internal/manager/storage/agent_conversation.go:199-230 - upsertAgentProfile
internal/manager/storage/agent_conversation.go:1505 - deterministic profile ID
internal/manager/agent_conversation_handlers.go:290-320 - entry point
```

## Proposed fix

In both stores: when `profile_id` resolves to an existing profile owned by
someone else, return a conflict error instead of updating; validate
`represents_id` against the agent owner (user) or team membership (team).

## Resolution

`UpsertRepresentativeAgent` in both stores now validates the represents
subject: `user` requires the caller's own user ID, `team` requires an active
membership of the agent owner in an active team, anything else is rejected.
Profile upserts are guarded by owner: postgres only updates a conflicting
profile when `owner_id` matches (conflict otherwise), and the memory store
refuses to touch profiles owned by someone else. Regression tests cover the
foreign profile conflict and both represents spoofing directions in
`internal/manager/storage/memory_lifecycle_test.go`.

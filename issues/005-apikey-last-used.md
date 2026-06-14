# ISSUE-005 User API key last_used_at never updated

**Status:** partial
**Severity:** medium
**Component:** auth / storage
**Found:** 2026-06-13
**Resolved:** -

## Summary

The `user_api_keys` table has a `last_used_at` column but no code path ever writes to it. API keys are authenticated through the regular user auth flow (CF Access JWT), not through user API keys. The user API key mechanism exists for potential programmatic access but is never exercised against any endpoint, so `last_used_at` remains NULL forever.

## Round-trip affected

- [x] User login / API key (credential hygiene)

## Current behavior

`db/init.sql:150-159`:
```sql
CREATE TABLE IF NOT EXISTS user_api_keys (
    ...
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);
```

`storage/postgres_store.go` - `CreateUserAPIKey` writes key_id, owner_user_id, name, key_hash, prefix, created_at. No update to last_used_at.

The store `AuthenticateAgent` method checks agent API keys (from `api_keys` table), not user API keys. There is no `AuthenticateUserAPIKey` method.

## Expected behavior

Either:
A. Add an authentication path for user API keys (e.g., `X-Pax-User-Key` or `Authorization: Bearer paxu_...`) that validates against `user_api_keys` and updates `last_used_at`
B. If user API keys aren't used for auth yet, document them as "future" and remove the dead column

## Impact

- `last_used_at` is dead data - always NULL
- No way to know which keys are actually in use
- No way to prune unused keys

## Affected code

```
internal/manager/storage/postgres_store.go - CreateUserAPIKey, no last_used_at update
internal/manager/auth/service.go - no user API key authentication path
internal/manager/security.go - protect() only handles CF JWT, not user API keys
```

## Proposed fix

Option A: Add `AuthenticateUserAPIKey(ctx, keyHash) (UserPrincipal, error)` to the store. Add an auth middleware that checks `X-Pax-User-Key` header when CF Access JWT is absent. Update `last_used_at` on successful auth.

Option B (simpler): Remove `last_used_at` column from schema until programmatic API key auth is implemented.

## Current note

The store now has user API key authentication support that updates
`last_used_at` on successful validation. The issue is still partial because
user-facing HTTP authentication remains Cloudflare Access based, and no public
user API key auth middleware has been enabled yet.

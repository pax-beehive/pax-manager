# ISSUE-006 No GET /api/user/me endpoint

**Status:** open  
**Severity:** medium  
**Component:** api  
**Found:** 2026-06-13  
**Resolved:** —

## Summary

There is no endpoint for the dashboard to query the current user's profile information (email, display name, role, admin status). The frontend currently has no way to display "Logged in as todd@..." or determine if the user should see admin features.

## Round-trip affected

- [x] User login / API key (dashboard UX)

## Current behavior

The auth flow (`auth.Service.Principal`) resolves the user from CF Access JWT on every request, creating the user record if needed. But this information is consumed internally and never exposed to the frontend.

`GET /api/user/agents` and other user endpoints return data scoped to the principal, but the principal itself (who am I?) is not accessible.

## Expected behavior

`GET /api/user/me` returns:
```json
{
  "data": {
    "user_id": "usr_xxx",
    "email": "todd@example.com",
    "display_name": "Todd",
    "role": "admin",
    "is_admin": true
  },
  "code": 200,
  "message": "ok"
}
```

## Impact

- Dashboard can't show current user identity
- Admin features can't be conditionally rendered
- No way to verify "who am I logged in as" without inspecting network tab

## Affected code

```
internal/transport/http/router/paxmanager/api/pax_manager.go — no /api/user/me route
internal/manager/idl_handlers.go — no handler
internal/manager/api_facing.go — no handleUserMe
```

## Proposed fix

Add `GET /api/user/me` to the Thrift IDL. Handler calls `userapi.GetMe` which runs `Principal()` and returns the UserPrincipal. No new store methods needed.

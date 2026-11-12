# [ISSUE-083] Native harness status omits the logged-in account

**Status:** resolved
**Severity:** medium
**Component:** api / auth / node control
**Found:** 2026-11-12
**Resolved:** 2026-11-12

## Summary

The native harness user API showed login state and method but discarded the
account identity reported by an updated paxd.

## Resolution

The current node user API now passes an optional typed `account` object with
email, display name, organization, and plan. Only logged-in and succeeded states
can expose account metadata. Unknown provider fields are excluded, unsafe or
oversized strings are omitted, and empty account objects are omitted.

All fields are optional, so older daemons and API-key credentials without a
reported identity keep working. No account metadata is persisted or logged.
This change does not add identity discovery to the legacy agent API or inspect
native credential files; native identity extraction belongs to paxd.

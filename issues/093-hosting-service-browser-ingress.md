# [ISSUE-093] Hosting service and owner routes missing from browser Worker

**Status:** resolved
**Severity:** high
**Component:** auth / Worker / hosting
**Found:** 2026-11-18
**Resolved:** 2026-11-18

## Summary

The active browser Worker had no machine accounting/job ingress and rejected
the existing authenticated Starter owner paths. A separate gateway could not
reach those Manager operations through the deployed edge bindings.

## Resolution

The existing Worker accepts four exact POST service routes through a distinct,
default-disabled branch. Explicit US selection reuses the existing US Manager
origin and Access pair while preserving the caller's own Manager role key.
It never creates user assertions, reads D1 or supplies an application key.
Bodies, deadlines and replies are bounded; redirects and arbitrary headers
are rejected.

The existing authenticated browser/native owner flows also allow exact
GET/POST Starter and POST wake. They keep their true authentication, owner
resolution, same-origin checks and existing method/path signatures. No Manager
schema, aliases or generated code changed. This applies to current hosting and
owner APIs; it does not open legacy agent or general node routes.

Production activation and real-owner commissioning remain operator actions.
The checked-in region value is empty; this issue records the code fix.

## Verification

Worker tests cover exact routing, wrong roles, identity/header isolation,
default disablement, bounded transport and failures. Real workerd fixtures
exercise all four machine paths, WorkOS browser/native owner authentication,
unchanged bodies and HMAC method/path verification using mock origins.

# Native clients cannot reach device login behind origin Access

- Status: resolved
- Resolved: 2026-10-13
- Severity: high
- Component: Worker / native client / auth

## Problem

Android receives an interactive Access redirect before regional Manager device
login. The public gateway accepts browser sessions or Node Keys, neither of which
is an Android user API key.

## Resolution

A disabled-by-default, explicitly allowlisted native gateway is implemented and
passes the full Worker suite (144 tests). Production activation is complete;
Android US/HK start/poll and unauthorized access checks passed. HK confirmation
URLs now use the canonical WorkOS page. Regional Access and Manager key
authorization are preserved. User-confirmed Android login completed through
credential commit/save/ack. Authenticated device/session reads, live Claude
prompt/reply and test-session stop passed. This resolves native gateway access;
full encrypted-client acceptance remains separate. Current Worker version: `e91fd9c2-53ca-40ec-b52e-4abe52ab526a`.
See [handoff](../docs/handoff_20261013_200000.md).

## Superseded authentication model

The transport issue was resolved, but paxl login was not an appropriate mobile
identity model because it creates a node. Issue 038 replaces Android account
login with WorkOS public-client PKCE. The old native device-login dispatcher now
returns 410; existing key routes remain for migration. Current deployment is
recorded in the issue 038 handoff.

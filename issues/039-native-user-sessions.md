# Native user sessions must not enroll nodes

- Status: partial
- Resolved: -
- Severity: high
- Component: Worker / Android / auth

## Problem

Android reused paxl device login, whose issue transaction inserts an offline
node and a user API key. A phone running the workspace UI is a user client,
like Web, and must not enroll a node to sign in.

## Current note

The native client gateway verifies WorkOS access tokens, binds the immutable
subject using the Web directory and forwards the existing signed user assertion
to the assigned regional Manager. Bootstrap ensures only a user. The fixed
HTTPS callback relays native PKCE codes to the Android scheme without affecting
Web cookies. Logout revokes the verified WorkOS session. Old native paxl login
is rejected with 410.

159 Worker tests, typecheck and deployment preflight pass. Production version
`59e2dd0e-1ff9-4c61-b20b-50cac28a92a9` is active. Anonymous/new bearer boundaries
and legacy 410 responses are verified live. Web authenticated session reload
passes. Real Pixel PKCE login acceptance is pending user interaction; historical
offline nodes are not removed by revoking their associated user key.

See [handoff](../docs/handoff_20261013_203000.md).

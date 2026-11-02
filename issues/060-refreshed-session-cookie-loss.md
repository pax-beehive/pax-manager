# [ISSUE-060] Backend failures discard refreshed browser session cookies

**Status:** resolved
**Severity:** high
**Component:** auth / Worker / regional gateway
**Found:** 2026-11-02
**Resolved:** 2026-11-02

## Summary

A successful WorkOS refresh can rotate the refresh token before a later directory
or upstream failure causes the replacement browser cookie to be discarded.

## Round-trip affected

- [x] User login / API key
- [ ] Agent registration / connection
- [ ] User-agent messaging round-trip

## Current behavior

Previously, shared identity lookup failures after refresh and account lookup
failures in `/auth/session` returned 503 without the replacement cookies. The
regional Node gateway also lost them on directory errors and proxy connection
errors. After the provider's replay grace period, the browser could no longer
refresh using its retired token and had to sign in again.

The ordinary Cloudflare Worker origin-proxy failure path already attached the
replacement cookies to its caught 503 response. That path is not changed into a
different authentication outcome.

## Expected behavior

Return replacement cookies after a successful refresh even when a subsequent
dependency fails. Continue returning the dependency failure, without granting
access or clearing a valid session. Terminal authentication failures still clear
the browser cookies and return 401.

## Resolution

- Shared authentication carries refreshed cookies through identity failures.
- Auth routes and Worker wrappers retain them on later unexpected failures.
- Regional account and HTTP or WebSocket proxy error responses retain them as well.
- Regression tests cover the previously failing paths and logout-provider errors.

This fixes a demonstrated failure mode; it does not establish that this was the
cause of every production sign-in prompt. Production incident correlation remains
separate from the deterministic regression tests.

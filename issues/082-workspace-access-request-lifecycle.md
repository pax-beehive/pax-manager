# [ISSUE-082] Workspace search requests lack status, cancellation and pending deadlines

**Status:** resolved
**Severity:** medium
**Component:** api / auth / storage / MCP
**Found:** 2026-11-11
**Resolved:** 2026-11-11

## Summary

A nonblocking search-expansion request returned an approval ID but exposed no
status or cancellation MCP. Its pending lifetime was conflated with the grant
lifetime, so agents could not reliably manage delayed approval.

## Round-trip affected

- [x] User-agent messaging round-trip

## Expected behavior

Requests have a configurable pending timeout, including zero for no time limit.
An agent can check status by ID, work independently, or explicitly cancel.
Approval within the pending deadline creates a turn-bound grant; cancelled,
expired and finished-turn requests cannot be approved later.

## Resolution

Added ID-based status and cancellation operations, a configurable pending TTL
(default 600 seconds; zero disables the time deadline), and five explicit states.
Approved grants receive their own bounded lifetime and still end with the turn.
Storage row locking and decision guards reject late approval after cancellation,
expiry or loss of the registered turn. Revoked requests cannot send queued
notifications or remain actionable Console cards.

Runtime suspension and automatic resume are outside this change. Agents can
work independently, poll status, or explicitly cancel; ending a turn abandons
its requests. No live service was modified.

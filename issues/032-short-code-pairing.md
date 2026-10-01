# 032: Short-code device pairing relay

- Status: resolved
- Resolved: 2026-10-01
- Severity: medium
- Component: e2ee / storage / user API

## Problem

Cross-device authorization requires copying a long high-entropy secret. Simply
replacing that secret with eight digits would make the legacy public commitment
an offline guessing oracle. Short-lived codes also need durable deadlines and
rate limits shared by all Manager replicas.

## Resolution

Added an opt-in `short-code-v2` relay for browser-to-browser OPAQUE, transactional
handshake/approval states, role capabilities and shared account/agent budgets.
Manager never stores the code or its password-derived verifier. The legacy node
and user pairing protocol, approved package format and recovery remain supported.
The companion Console implements the PAKE, numeric code UI and key delivery.

The migration is additive; no root keys or delivered packages are removed.
Lifecycle, HTTP ownership, PostgreSQL migration, concurrency and race tests cover
the new behavior. The application protocol still needs independent security
review; the underlying library review does not constitute such an audit.

# Encrypted sessions replay excessive history

- Status: partial
- Resolved: -
- Severity: high
- Component: e2ee / storage / Console

## Problem

Encrypted sessions previously restored canonical history while replaying the
event journal from cursor zero. Repeated history polling and browser-side prompt
context scans added decryption, normalization, and memory costs on long sessions.

## Current note

The existing encrypted-events user endpoint now supports JSON `view=replay`.
New paxd events carry opaque `turn_ref` metadata. Manager selects the latest
indexed turn, pages its ciphertext within a frozen head cursor, and supports
earlier turns by their start cursor. SSE resumes after that head. No server-side
ACP decryption or canonical message/part projection is required for new turns.

Old events have no outer turn reference. They remain readable as one legacy
bucket, which can still be large; this change does not retroactively repair
their indexing. The new contract applies to durable E2EE transport, not the
ordinary plaintext conversation endpoints or legacy plaintext agent API.

## Verification

BDD tests cover complete latest/older turns, bounded page continuations, arrivals
during replay, reconnect suffixes, owner isolation, duplicate metadata conflicts,
unindexed lifecycle tails, and consistent snapshots under concurrent PostgreSQL
commits. The migration is exercised twice against isolated PostgreSQL.

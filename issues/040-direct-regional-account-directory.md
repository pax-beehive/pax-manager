# 040: Remove central routing from fixed regional account access

- Status: partial
- Resolved: -
- Severity: high
- Component: regional gateway / identity directory / Console

## Problem

An HK account reaching the central Cloudflare gateway adds an avoidable network
hop. Removing that gateway also removes D1 account discovery: a miss in HK does
not establish whether an account exists in US. Browser hints alone cannot restore
the correct account on another device or safely allocate a new user ID.

## Implemented

Each region has a complete durable PostgreSQL account directory. One authority
allocates immutable identities and routes, with a committed change journal,
background replication and authority recovery on local misses. Local hits require
no remote directory. A Node gateway shares WorkOS auth and Manager HMAC identity
with the existing Worker, streams local traffic and returns the fixed destination
for foreign accounts. Console direct mode remembers the region as a hint, performs
one authoritative bootstrap and never periodically probes/reassigns an account.

## Current note

Code and automated validation are implemented. Production D1 import, row-level
comparison, DNS/TLS/WorkOS registration, traffic cutover and Android persisted-URL
verification remain deployment work. Old and new directories must not allocate
accounts concurrently. No live configuration or production data was changed.
See `workers/region-directory/regional/README.md` for rollout and rollback.

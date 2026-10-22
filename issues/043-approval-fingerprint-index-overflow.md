# [ISSUE-043] Long approval fingerprints overflow the grant lookup index

**Status:** resolved
**Severity:** high
**Component:** storage
**Found:** 2026-10-21
**Resolved:** 2026-10-21

## Summary

Tool approval fingerprints can contain complete commands. Indexing that unbounded text in the grant lookup B-tree makes sufficiently long permission requests fail with SQLSTATE 54000.

## Resolution

Replace the raw-fingerprint index with `idx_agent_approvals_grant_lookup_digest`, using `md5(action_fingerprint)`. Create the replacement before dropping the old index. Grant lookup uses the same expression and still requires exact equality of the full fingerprint, preserving existing grants and preventing digest collisions from authorizing another command. No fingerprint values or approval payloads are rewritten.

The startup schema migration is idempotent. Production deployment is pending; the issue is resolved in source only. This does not establish the cause of intermittent public-site timeouts.

## Verification

A real PostgreSQL 16 regression first failed with the legacy index, then passed with a 16 KiB incompressible fingerprint, legacy grant reuse, nonmatching command and session rejection, repeated migration, old-index removal, and an EXPLAIN check of the new index. The modified Go lookup function has 100% statement coverage in that regression.

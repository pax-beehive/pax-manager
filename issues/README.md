# Issues

Pax-manager issues discovered during round-trip API flow review (2026-06-13).

| # | Status | Title | Severity | Component |
|---|---|---|---|---|
| 001 | resolved | [Agent-to-user outbound message endpoint missing](001-outbound-message-endpoint.md) | blocker | api / mailbox |
| 002 | resolved | [Session auto-creation on first message](002-session-auto-creation.md) | blocker | session / mailbox |
| 003 | resolved | [Missing mailbox "delivered" status transition](003-delivered-status.md) | high | mailbox |
| 004 | open | [Register response wrapped in envelope - paxd can't parse](004-register-response-wrapping.md) | high | api |
| 005 | partial | [User API key last_used_at never updated](005-apikey-last-used.md) | medium | auth / storage |
| 006 | resolved | [No GET /api/user/me endpoint](006-get-me-endpoint.md) | medium | api |
| 007 | open | [Mailbox pull response envelope wrapping breaks HTTP fallback](007-mailbox-envelope-wrapping.md) | high | api |
| 008 | resolved | [Token usage model too narrow vs paxd reporting](008-token-usage-model.md) | medium | session / storage |
| 009 | resolved | [Node status report can hijack any agent by ID (cross-tenant)](009-node-status-agent-hijack.md) | blocker | storage / auth |
| 010 | resolved | [Caller-supplied conversation_id joins any existing conversation (cross-tenant)](010-conversation-id-membership-graft.md) | blocker | storage / auth |
| 011 | resolved | [User-controlled artifact bucket/object gets server-signed GCS URLs (confused deputy)](011-artifact-content-gcs-signing.md) | high | storage / api |
| 012 | resolved | [Representative agent upsert can overwrite another user's profile and impersonate owners](012-representative-agent-profile-takeover.md) | medium | storage / auth |
| 013 | resolved | [Session creation stamps caller-supplied conversation_id (cross-tenant write injection)](013-session-conversation-id-injection.md) | medium | session / storage |
| 014 | resolved | [ACP user tunnel claim races agent tunnel registration](014-acp-user-tunnel-claim-race.md) | medium | session / api |
| 015 | resolved | [ACP request ID reuse corrupts long session history](015-acp-request-id-reuse-corrupts-history.md) | high | session / storage |
| 018 | resolved | [Detached permission requests have no actionable approval ID](018-detached-permission-requests.md) | high | session |
| 019 | resolved | [Message persistence deadlocks with runtime snapshots](019-message-runtime-deadlock.md) | high | storage |
| 020 | resolved | [ACP transport loses the Manager turn identity](020-acp-turn-identity.md) | high | session / storage |
| 021 | resolved | [S3 presigned paths are encoded twice during signing](021-s3-presigned-path-double-encoding.md) | high | storage |
| 022 | partial | [Large tool payloads inflate session history reads](022-large-session-history.md) | high | userapi / storage |
| 023 | resolved | [Observer replay has no consistent turn or message contract](023-observer-turn-contract.md) | high | observer / storage / console |
| 024 | resolved | [Summary history loses prompts and long-turn replies](024-summary-history-missing-text.md) | high | history / console |
| 025 | resolved | [Tagged turns collapse distinct text segments](025-tagged-turn-text-segments.md) | high | history / console |
| 026 | resolved | [Message detail reads select the regex substring overload](026-message-detail-substring-overload.md) | high | storage / userapi |
| 027 | resolved | [CI fails formatting, lint, and object storage startup](027-ci-baseline.md) | high | deployment |
| 028 | resolved | [Queued turns depend on the originating browser stream](028-snapshot-driven-turn-queue.md) | high | session / storage |

| 029 | resolved | [Completed session history calibration](029-session-history-calibration.md) | high | session / storage / api |

| 030 | resolved | [Concurrent history text flushes reorder chunks](030-history-text-write-order.md) | high | history |
| 031 | resolved | [Permission cache invalidated by daemon version](031-permission-cache-daemon-version.md) | medium | runtime / permissions |

| 032 | resolved | [Short-code device pairing relay](032-short-code-pairing.md) | medium | e2ee / storage / user API |

| 033 | partial | [Encrypted sessions replay excessive history](033-encrypted-turn-replay.md) | high | e2ee / storage / Console |

See [TEMPLATE.md](TEMPLATE.md) for the issue format.

## Launch readiness

| # | Status | Title | Severity | Component |
|---|---|---|---|---|
| 016 | resolved | [Public Node API exposure needs a reviewed launch boundary](016-public-node-api-launch-boundary.md) | blocker | deployment / auth |
| 017 | partial | [Trusted proxy and origin boundary](017-trusted-proxy-origin-boundary.md) | high | security / deployment |

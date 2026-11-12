# Issues

Pax-manager issues discovered during round-trip API flow review (2026-06-13).

Issue 067 also tracks the per-source task controls; rollout remains pending.
See [task-control handoff](../docs/handoff_20261104_230000.md).

| #   | Status   | Title                                                                                                                                   | Severity | Component                    |
| --- | -------- | --------------------------------------------------------------------------------------------------------------------------------------- | -------- | ---------------------------- |
| 083 | resolved | [Default workspace search scope stops at one agent](083-workspace-search-node-scope.md) | medium | auth / search / storage / MCP |
| 073 | resolved | [Stable historical text waits forever and inflates the visible queue](073-secretary-stable-history-and-pending-count.md) | high | Secretary / storage / status |
| 060 | resolved | [Backend failures discard refreshed browser session cookies](060-refreshed-session-cookie-loss.md) | high | auth / Worker / regional gateway |
| 061 | resolved | [Secretary results cannot be browsed by ordinary owners](061-summary-results-not-browsable.md) | medium | userapi / storage / Console |
| 064 | open | [Upstream background runtime snapshots can block Secretary sources](064-secretary-stale-upstream-background-state.md) | medium | runtime state / session scheduling |
| 063 | resolved | [Secretary replays rejected completed output indefinitely](063-secretary-invalid-output-retry-loop.md) | high | Secretary / validation / scheduling |
| 062 | resolved | [Search repeats a session as title, summary and message results](062-search-repeats-session-results.md) | medium | search / userapi / storage / Console |
| 044 | resolved | [Completed transport journal history grows without bound](044-transport-journal-retention.md) | high | storage / transport |
| 001 | resolved | [Agent-to-user outbound message endpoint missing](001-outbound-message-endpoint.md)                                                     | blocker  | api / mailbox                |
| 002 | resolved | [Session auto-creation on first message](002-session-auto-creation.md)                                                                  | blocker  | session / mailbox            |
| 003 | resolved | [Missing mailbox "delivered" status transition](003-delivered-status.md)                                                                | high     | mailbox                      |
| 004 | open     | [Register response wrapped in envelope - paxd can't parse](004-register-response-wrapping.md)                                           | high     | api                          |
| 005 | partial  | [User API key last_used_at never updated](005-apikey-last-used.md)                                                                      | medium   | auth / storage               |
| 006 | resolved | [No GET /api/user/me endpoint](006-get-me-endpoint.md)                                                                                  | medium   | api                          |
| 007 | open     | [Mailbox pull response envelope wrapping breaks HTTP fallback](007-mailbox-envelope-wrapping.md)                                        | high     | api                          |
| 008 | resolved | [Token usage model too narrow vs paxd reporting](008-token-usage-model.md)                                                              | medium   | session / storage            |
| 009 | resolved | [Node status report can hijack any agent by ID (cross-tenant)](009-node-status-agent-hijack.md)                                         | blocker  | storage / auth               |
| 010 | resolved | [Caller-supplied conversation_id joins any existing conversation (cross-tenant)](010-conversation-id-membership-graft.md)               | blocker  | storage / auth               |
| 011 | resolved | [User-controlled artifact bucket/object gets server-signed GCS URLs (confused deputy)](011-artifact-content-gcs-signing.md)             | high     | storage / api                |
| 012 | resolved | [Representative agent upsert can overwrite another user's profile and impersonate owners](012-representative-agent-profile-takeover.md) | medium   | storage / auth               |
| 013 | resolved | [Session creation stamps caller-supplied conversation_id (cross-tenant write injection)](013-session-conversation-id-injection.md)      | medium   | session / storage            |
| 014 | resolved | [ACP user tunnel claim races agent tunnel registration](014-acp-user-tunnel-claim-race.md)                                              | medium   | session / api                |
| 015 | resolved | [ACP request ID reuse corrupts long session history](015-acp-request-id-reuse-corrupts-history.md)                                      | high     | session / storage            |
| 018 | resolved | [Detached permission requests have no actionable approval ID](018-detached-permission-requests.md)                                      | high     | session                      |
| 019 | resolved | [Message persistence deadlocks with runtime snapshots](019-message-runtime-deadlock.md)                                                 | high     | storage                      |
| 020 | resolved | [ACP transport loses the Manager turn identity](020-acp-turn-identity.md)                                                               | high     | session / storage            |
| 021 | resolved | [S3 presigned paths are encoded twice during signing](021-s3-presigned-path-double-encoding.md)                                         | high     | storage                      |
| 022 | partial  | [Large tool payloads inflate session history reads](022-large-session-history.md)                                                       | high     | userapi / storage            |
| 023 | resolved | [Observer replay has no consistent turn or message contract](023-observer-turn-contract.md)                                             | high     | observer / storage / console |
| 024 | resolved | [Summary history loses prompts and long-turn replies](024-summary-history-missing-text.md)                                              | high     | history / console            |
| 025 | resolved | [Tagged turns collapse distinct text segments](025-tagged-turn-text-segments.md)                                                        | high     | history / console            |
| 026 | resolved | [Message detail reads select the regex substring overload](026-message-detail-substring-overload.md)                                    | high     | storage / userapi            |
| 027 | resolved | [CI fails formatting, lint, and object storage startup](027-ci-baseline.md)                                                             | high     | deployment                   |
| 028 | resolved | [Queued turns depend on the originating browser stream](028-snapshot-driven-turn-queue.md)                                              | high     | session / storage            |

| 029 | resolved | [Completed session history calibration](029-session-history-calibration.md) | high | session / storage / api |

| 030 | resolved | [Concurrent history text flushes reorder chunks](030-history-text-write-order.md) | high | history |
| 031 | resolved | [Permission cache invalidated by daemon version](031-permission-cache-daemon-version.md) | medium | runtime / permissions |

| 032 | resolved | [Short-code device pairing relay](032-short-code-pairing.md) | medium | e2ee / storage / user API |

| 033 | partial | [Encrypted sessions replay excessive history](033-encrypted-turn-replay.md) | high | e2ee / storage / Console |

| 034 | partial | [Regional directory, browser routing and machine hint recovery (paxl download fixed)](034-regional-user-directory.md) | high | auth / storage / Worker |

| 035 | partial | [Regional paxl login races (KEV-90)](035-paxl-regional-login.md) | high | auth / storage / Worker / paxl / Console |

| 036 | partial | [Attachment-only prompts disappear from history](036-attachment-only-prompt-history.md) | high | history / ACP projection |

| 037 | resolved | [Native gateway and account round trip verified](037-native-client-gateway.md) | high | Worker / native client / auth |

| 038 | resolved | [Unicode artifact filenames break signed downloads](038-unicode-artifact-download-filename.md) | high | userapi / object storage |

| 039 | partial | [Native user sessions must not enroll nodes](039-native-user-sessions.md) | high | Worker / Android / auth |

| 040 | partial | [Direct regional account directory](040-direct-regional-account-directory.md) | high | gateway / identity / Console |

| 041 | resolved | [Legacy ACP model catalogs cannot be changed](041-legacy-model-selection.md) | medium | session / userapi / Console |

| 042 | resolved | [OpenClaw session creation rejects per-session MCP injection](042-openclaw-session-mcp.md) | high | session / ACP |

| 043 | partial | [Turn and manual approval notifications, reply previews and subscription dashboard](043-turn-completion-notifications.md) | medium | notifications / storage / user API / Console |

| 043 | resolved | [Long approval fingerprints overflow the grant lookup index](043-approval-fingerprint-index-overflow.md) | high | storage |

| 045 | resolved | [Late artifact publications appear after their turn footer](045-artifact-turn-placement.md) | medium | paxd / history / storage / Console |
| 046 | open | [Legacy container test assumes an active mailbox poller](046-legacy-container-mailbox-runtime.md) | medium | integration / deployment |
| 047 | resolved | [Legacy journal conversions still scan history during startup](047-legacy-journal-startup-backfills.md) | high | storage / startup |
| 048 | resolved | [VM deployment leaves workspace search disabled](048-vm-search-not-enabled.md) | high | deployment / search |
| 049 | open | [Production Console has no workspace search page](049-production-console-search-route.md) | medium | deployment / Console |
| 051 | resolved | [Search backfill repeatedly sorts and scans unrelated messages](051-search-backfill-query-indexes.md) | high | storage / search indexing |
| 052 | resolved | [Substring search ignores identifier boundaries and relevance tiers](052-search-literal-ranking.md) | medium | search |

| 053 | resolved | [Opening an unlisted session requires scanning session pages](053-canonical-session-detail.md) | medium | userapi / session metadata |

| 054 | resolved | [Search treats space-separated keywords as one phrase](054-search-multiple-keywords.md) | medium | search / userapi / MCP |

| 055 | resolved | [Secretary work sessions flood ordinary session lists](055-secretary-work-session-list.md) | medium | storage / session |

| 056 | resolved | [Secretary progress reads can exhaust database I/O](056-secretary-progress-runaway-query.md) | high | storage / Secretary |

| 057 | resolved | [Rolling summary inputs expose fields rejected in generated output](057-secretary-baseline-output-schema-conflict.md) | high | Secretary / summary validation |

| 058 | resolved | [Raw-event limits fragment Secretary model batches](058-secretary-raw-event-batch-fragmentation.md) | medium | storage / Secretary batching |

| 059 | resolved | [Automatic Secretary bypasses compact input views](059-secretary-default-input-view.md) | high | Secretary / prompt / source reads |

| 050 | resolved | [Historical message backfill starves titles and live changes](050-search-backfill-starvation.md) | high | storage / search indexing |

| 065 | resolved | [Search acceptance still requires the former FIFO claim index](065-search-acceptance-stale-fifo-plan.md) | medium | storage / search acceptance |

| 066 | resolved | [Secretary startup requeues previously processed sessions](066-secretary-startup-requeues-completed-sessions.md) | medium | storage / Secretary bootstrap |

| 067 | partial | [Secretary summary selection, capacity and execution lifecycle](067-secretary-topic-capacity-and-execution-lifecycle.md) | high | Secretary / summary / lifecycle |

| 068 | open | [Legacy Paxd container fixture uses obsolete bootstrap and Hermes transport](068-legacy-paxd-container-integration-fixture.md) | medium | integration / Paxd harness |

| 069 | resolved | [Pax MCP calls interrupt manual Secretary executions](069-pax-mcp-manual-approval-blocks-secretary.md) | high | ACP / MCP / Secretary |

| 070 | open | [Pax MCP automatic approval does not verify the effective registration origin](070-pax-mcp-origin-binding.md) | high | ACP / MCP / auth |

| 071 | resolved | [Secretary source-completion waits appear queued](071-secretary-source-wait-reason-not-persisted.md) | medium | Secretary / storage / task status |

| 072 | partial | [DSCODE long terminal input can block a Secretary execution](072-dscode-long-pty-command-blocks-secretary.md) | high | DSCODE runtime / terminal / Secretary |

| 074 | partial | [Secretary canceled executions remain unknown](074-secretary-canceled-executions-remain-unknown.md) | high | Secretary / ACP terminal projection / recovery |

| 075 | partial | [Secretary rejects shortened baseline evidence quotes](075-secretary-shortened-baseline-quotes-rejected.md) | high | Secretary / evidence binding |

| 076 | open | [Secretary compares a read receipt as message identity](076-secretary-read-receipt-compared-as-identity.md) | medium | Secretary / evidence read proof |

| 077 | partial | [Search summary source checks repeat display replacement parsing](077-search-summary-replacement-check-cost.md) | high | Search / summary source visibility |

| 078 | partial | [Secretary acceptance schema fixtures exceed the package timeout (template isolation and parallel CI)](078-secretary-acceptance-schema-fixture-timeout.md) | high | CI / Secretary acceptance fixtures |

| 079 | partial | [Secretary progress times out and conversation failures obscure overall work](079-secretary-progress-timeout-and-status-scope.md) | high | Secretary / progress SQL / status |

| 080 | partial | [Bulk Secretary retry times out after committing work](080-secretary-bulk-retry-response-timeout.md) | high | Secretary / bulk retry / acknowledgement |

| 081 | resolved | [Native harness login has no owner-scoped user API](081-native-harness-login-user-api.md) | medium | api / auth / node control |

| 082 | resolved | [Workspace search requests lack status, cancellation and pending deadlines](082-workspace-access-request-lifecycle.md) | medium | api / auth / storage / MCP |

See [TEMPLATE.md](TEMPLATE.md) for the issue format.

## Launch readiness

| #   | Status   | Title                                                                                               | Severity | Component             |
| --- | -------- | --------------------------------------------------------------------------------------------------- | -------- | --------------------- |
| 016 | resolved | [Public Node API exposure needs a reviewed launch boundary](016-public-node-api-launch-boundary.md) | blocker  | deployment / auth     |
| 017 | partial  | [Trusted proxy and origin boundary](017-trusted-proxy-origin-boundary.md)                           | high     | security / deployment |

2026-10-26 update for #043: optional original-event timestamps let Chrome for
Android show when a notification's event happened without changing its text.

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

See [TEMPLATE.md](TEMPLATE.md) for the issue format.

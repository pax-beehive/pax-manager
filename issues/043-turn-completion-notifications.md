# Turn completion notifications

- Status: partial
- Resolved: -
- Severity: medium
- Component: notifications / storage / user API / Console

## Problem

Users must reopen or refresh the Console to discover that a turn has finished.
The Android TWA enables notification delegation. The original implementation
had no subscription registry, Web Push sender, or device push handler.

## Current note

2026-10-26: notification payloads now include the original event time as optional
`occurred_at` Unix milliseconds. The companion Console uses the native notification
timestamp without changing notification text, delivery expiry, or read cleanup.
Existing queued payloads without the field retain their previous behavior.

2026-10-22: pending approvals now use `approval.required` notifications on
standard sessions. Pax Auto Approve is excluded at enqueue and delivery time;
saved grants bypass manual approval creation. The worker also suppresses
resolved, expired, or inaccessible requests. Clicking opens the session for
review and does not approve the request. Creation and PostgreSQL fanout share
one transaction. The configuration advertises `approval_required_enabled` for
the companion Console description. No database migration is required.

The module design and delivery order have been reviewed against current code.
Delivery 1 implements the notification core: subscription shape/key validation,
minimal versioned payload and session link, stable event/delivery identities,
owner-scoped fanout, receiver-revision checks, bounded retry and Web Push status
classification. These rules are now used by the registry and test delivery worker.

Scope now also includes an administrator-only Push subscriptions dashboard next
to the existing Customers page, covering all customers' receiver statuses with
US/HK region filters and canonical regional customer references. Delivery 2 now
ships this read-only dashboard with the receiver registry. Subscription state,
last reported device permission, and delivery outcome remain separate. Delivery
3 adds actual latest-attempt results; provider acceptance is not device display.

Deliveries 1-2 are implemented in the notification-subscriptions development
worktrees. PostgreSQL and memory storage, generated Thrift/Hertz/OpenAPI, owner
registration/revocation, the guarded regional read, and the Console dashboard
are connected. Revocation erases subscription keys. Browser expirationTime is
accepted and projected as expiry. No raw endpoint or keys appear in metadata.

Deliveries 1-2 were merged and deployed by the user. Delivery 3 is implemented
on `codex/web-push-notifications`: durable leased test tasks, a VAPID Web Push
sender, bounded retry, revision-conditional invalidation, real latest-attempt
metadata, device enable/disable/test controls, subscription repair and a
push-only service worker. Signing out revokes the owned browser receiver before
the ordinary gateway logout, with bounded local cleanup.

The owner merged delivery 3, backed up one VAPID bundle, and authorized its
configuration in both regional Managers. Both healthy Managers now persist that
bundle; the owner confirmed the test notification reaches the device.

Delivery 4 for standard sessions is implemented on `codex/turn-completion-push`:
normal `end_turn` responses correlate against durable prompt history, then commit
terminal history, the completion ledger and receiver fanout atomically. Browser
disconnect does not control task creation; replay uses the original inbox time,
does not extend TTL, and does not notify newly registered receivers. Business
write failures remain retryable in the reliable inbox. The delivery worker
rechecks current session ownership. Config reports automatic availability when
the sender is ready. Console shows the supported standard-session scope and uses
a dedicated transparent PAX badge for Android's status bar.

Delivery 4 is merged and the owner confirmed a real turn notification arrives.
The follow-up snapshots a bounded preview of the latest visible assistant reply
in the completed turn, excluding prompts, thoughts, tool output and other
identities. The companion Console displays that preview and recovers notification
click navigation when the selected window cannot be reused. Coverage includes
PostgreSQL isolation, absent replies, Unicode limits, rollback/retry and the
actual disconnected-browser ACP projection with flushed reply text.
The notification target follows Home's canonical `/?session_id={encoded_id}`
route. The worker converts older `/sessions/{id}` targets to the same Home view.

The streamed-preview follow-up reconstructs the last contiguous assistant text
segment across adjacent segment rows before taking its bounded prefix. This
fixes alerts containing only a final stream fragment. Legacy aggregate rows are
unchanged, and gaps/tool/thought boundaries cannot pull earlier commentary or
private output into the preview. Memory/PostgreSQL regression cases cover chunk
joining, boundaries, prefix limits and the actual ACP projection/outbox path.
The companion Console change dismisses an exact read turn on the current device
after its durable final reply is visible, without cross-device read sync or a
new Manager endpoint. The stable v1 notification identity remains unchanged.

Encrypted-session terminal signaling, retention/operational checks and final
notification-click device acceptance remain open. The Console explicitly states
that automatic alerts do not yet cover encrypted sessions. This follow-up is
not a production deployment.

This file uses issue 043, matching the existing index; issue 042 belongs to the
independent OpenClaw per-session MCP fix.

See [design and review](../docs/turn_completion_notifications_design.md).

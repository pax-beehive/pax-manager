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

Encrypted-session terminal signaling, retention/operational checks and final
real-turn/device release acceptance remain open. The Console explicitly states
that automatic alerts do not yet cover encrypted sessions. This PR is not a
production deployment and does not claim OS display for an actual completed turn.

This file uses issue 043, matching the existing index; issue 042 belongs to the
independent OpenClaw per-session MCP fix.

See [design and review](../docs/turn_completion_notifications_design.md).

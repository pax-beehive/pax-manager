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

Deliveries 4-5 remain open: canonical turn-completion integration, atomic event
fanout, E2EE terminal signaling, retention/operational checks and real-device
release validation. Public config explicitly reports `turn_completion_enabled`
false; UI does not promise automatic turn alerts. Browser fixtures exercised the
installed worker without an app page, but native OS display and an installed
Android TWA still need validation after deployment and secret configuration.
No production deployment, production secret change or real push was performed
by the agent in delivery 3.

See [design and review](../docs/turn_completion_notifications_design.md).

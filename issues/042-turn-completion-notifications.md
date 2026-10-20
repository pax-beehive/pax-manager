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
classification. The core has no database/network side effects and is not yet
wired into the running Manager.

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

Deliveries 3-5 remain open: durable outbox/worker, real Web Push adapter, Console
permission/subscription controls and service worker, canonical turn-completion
integration, and real-device release validation.
E2EE needs a reviewed minimal terminal signal or an explicit release limitation.
The dashboard is implemented locally. No notification has been sent and no
production deployment has changed.

See [design and review](../docs/turn_completion_notifications_design.md).

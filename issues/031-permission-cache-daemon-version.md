# Permission cache invalidated by daemon version

- Status: resolved
- Resolved: 2026-09-29
- Severity: medium
- Component: runtime identity / permissions / storage

## Problem

The permission observation key included the entire ACP client initialize hash,
including `clientInfo.version`. Upgrading paxd hid unexpired native permission
choices even when the ACP adapter and underlying configuration were unchanged.

## Resolution

A separate configuration fingerprint uses the actual client capability hash,
protocol, adapter/runtime implementation, launch command, and worker response.
The full runtime identity remains available for diagnostics. Daemon version-only
upgrades no longer invalidate new-format permission observations.

Existing legacy observations can be displayed as unverified suggestions for the
same agent. They cannot authorize existing-session changes. Session creation
continues to validate and apply choices against its real ACP response. Negative,
expired, mixed-pool, and known incompatible records retain conservative handling.

The additive database migration and capability-report extension are backward
compatible. Source changes require a Manager rollout and a paxd update for stable
future configuration keys; no production deployment is included in this fix.

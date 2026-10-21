# Turn completion notifications

Status: deliveries 1-3 are merged. The owner confirmed test pushes arrive after
the backed-up VAPID bundle was configured in both regional Managers. Standard
session completion integration is implemented on `codex/turn-completion-push`.
Encrypted-session completion and final real-turn/device acceptance remain open.

Scope update, 2026-10-19: include an administrator dashboard for all notification
subscriptions alongside the existing Customers dashboard. Deliver its registry
view with delivery 2, then populate real delivery outcomes in delivery 3.

## Outcome and first release

After a user enables notifications on a browser or the Android TWA, a completed
turn produces a system notification. Clicking it opens the authorized session.
The originating tab does not need to remain open. Device connectivity, browser
policy, and OS permissions still govern delivery and display.

The first release implements standard Web Push. The existing Android package is
a Bubblewrap TWA with `enableNotifications: true`; it does not need a new native
FCM integration for this release. Native FCM, APNs, and Android vendor channels
are later adapters. A standalone notification deployment is unnecessary now.

The initial setting is per receiver: off until explicitly enabled, then notify
for each normally completed turn on every enabled receiver. Do not add presence
tracking or an unreliable background-only promise. Cancellation, failure, and
approval reminders are separate event types and are not emitted as completion.

Notifications use a generic title/body without prompts, replies, cwd, or session
names. The payload includes an opaque notification ID and a relative session
link. The existing authorized session API remains the source of actual results.

## Verified integration points

- `internal/manager/conversation.go`: `persistConversationTurnDone` flushes
  history text, upserts a `turn_done` message, and writes its raw message part.
  `writeConversationTurnDone` writes the browser SSE only after persistence.
- `internal/manager/storage/message_history.go`: PostgreSQL message upsert uses
  a data-modifying CTE; the memory store uses a mutex. These are viable atomic
  boundaries for inserting completion records alongside durable messages.
- `internal/manager/acp_pipeline.go`: prompt responses are correlated with
  tracked prompt requests and expose `stopReason`. Use the business turn ID,
  not the request ID, as notification identity.
- `internal/manager/node_control_tunnel.go`: active-turn snapshots drive runtime
  status and queued dispatch. Absence from a snapshot is not proof of successful
  completion and must not trigger a notification.
- `pax-console/src/app/api/pax/[...path]/route.ts`: browser REST goes through the
  existing same-origin authenticated proxy and regional routing.
- `pax-console/src/app/sessions/[sessionId]/page.tsx`: the current session route
  accepts `agentId` and `nodeId` query parameters.
- E2EE events and history are opaque to the Manager. An encrypted `turn_done`
  written by paxd is not a Manager-readable completion event.
- The newer Console `origin/main` contains `/admin/customers`,
  `components/analytics/customer-analytics-page.tsx`, and the administrator-only
  Customers entry in the lower sidebar group. The existing local Console main
  checkout predates these files and contains unrelated uncommitted changes.
- The newer Manager `origin/main` contains `customer_analytics.go` and the
  regional gateway's `customer-analytics.ts`. Both regional Managers authorize
  the original administrator identity; the gateway exposes regional freshness
  and refuses the aggregate if either region denies authorization.

## Module boundaries

| Module | Responsibility | Location |
|---|---|---|
| Browser subscription | Explicit permission, service worker registration, subscribe/unsubscribe, registration repair | Console `features/notifications` |
| Receiver registry | Authenticated owner, subscriptions, revision, enable/revoke, VAPID public configuration | Manager `userapi` and `storage` |
| Administrator subscription view | All-customer metadata, status summaries, filtering, pagination, regional freshness | Manager read model, regional gateway, Console `analytics` |
| Completion projection | Correlated successful terminal event to one durable notification | Manager ACP/conversation persistence boundary |
| Notification core | Stable identities, payload, receiver eligibility, fanout, retry policy | Manager `notifications` |
| Delivery persistence | Atomic event/task creation, due jobs, leases, attempt results, retention | Manager `storage` |
| Channel worker | Claim due jobs, recheck receiver, Web Push adapter, record outcome | Manager background worker plus `notifications` adapter |
| Device receiver | Handle push, display a visible notification, validate click target | Console root-scoped service worker |

Business code supplies a server-resolved owner and canonical turn identity. It
does not know push endpoints or credentials. The notification core does not
import Manager transport handlers or database implementations. Storage may
import the core's value types; adapters return its canonical outcomes.

## Data and ownership

Use the current regional Manager database; do not introduce a second user store.

### `notification_receivers`

- Server-generated receiver ID; authenticated `owner_user_id`.
- Channel (`web_push` initially), optional device label, enabled flag.
- Web Push endpoint, `p256dh`, `auth`, endpoint fingerprint, VAPID key ID.
- Monotonic revision, optional browser expiration, created/updated/last-seen time.
- Unique endpoint fingerprint; never silently move a receiver between accounts.

The browser uploads a subscription, not an owner ID. The Manager derives owner
from the authenticated principal. API reads return receiver metadata without
the endpoint or encryption material. Enforce a per-user receiver limit.

Identical registration is idempotent and does not change revision. Changing
delivery material, disabling, or revoking increments revision. Account switch
must unsubscribe and create a fresh subscription before binding the new owner;
a conflicting existing binding returns 409. Logging out should revoke the
receiver while credentials are still valid, with browser unsubscribe as a local
fallback. A request already accepted by a provider cannot be recalled.

### `notifications`

- Stable ID from user, agent, session, and business turn ID.
- Owner, canonical scope, event type, original completion timestamp.
- Versioned minimal payload and expiration (one hour for this release).
- Unique event identity; replay must not refresh payload, creation time, or TTL.

### `notification_deliveries`

- Stable ID from notification ID and receiver ID; unique on that pair.
- Owner, receiver ID, expected receiver revision, channel.
- State, attempt count, next attempt, lease token/deadline, expiration.
- Last status/error category and provider acceptance timestamp; no raw endpoint
  or provider response bodies in logs.

The core exposes pending/accepted/failed/expired states. Storage adds lease
metadata and records disabled/revised/deleted receivers as a suppressed terminal
delivery. Receiver invalidation is conditional on the revision that was sent,
so a late 410 cannot revoke a newly refreshed subscription.

## REST contract

All paths are under `/api/v1/user/{user_id}` and the existing principal check.
Console accesses them through `/api/pax`. Prefer `self` for browser calls.

| Method/path | Result |
|---|---|
| `GET /notifications/config` | Enabled status, channel, VAPID public key and key ID |
| `PUT /notifications/receivers/web-push` | Validate and idempotently register current subscription; return receiver ID/revision |
| `GET /notifications/receivers` | Current user's receiver metadata |
| `DELETE /notifications/receivers/{receiver_id}` | Idempotently revoke an owned receiver |
| `POST /notifications/receivers/{receiver_id}/test` | Rate-limited test notification through the real queued delivery path |
| `GET /notification-subscriptions` (admin only, `self` route) | Regional, paginated subscription metadata and matching status totals across customers |

Generated API changes start in `api/pax_manager.thrift`, followed by
`make generate`. Keep transport handlers thin and OpenAPI synchronized. Do not
hand-edit generated handlers, routers, schemas, or mocks.

## Administrator dashboard alongside Customers

### Placement and access

Add `/admin/notifications` with the label `Push subscriptions` adjacent to
`Customers` in the existing lower administrator navigation group. Preserve the
current Customers placement. Both pages offer an adjacent Customers / Push
subscriptions page switcher so the relationship remains visible on mobile.

Reuse `AuthGate`, `ConsoleLayout`, the effective administrator view, and
`canSeeAdminFeatures`. Preview-as-user must hide the entry and deny dashboard
reads. The Manager independently requires `principal.IsAdmin` before reading
or returning any other customer's metadata. Ordinary users retain access only
to their own notification settings and receiver registry.

Implement against a checkout containing the existing Customers feature. Do not
replace or downgrade the dirty local Console checkout to add the new page.
The delivery 1 notification core must accompany the delivery 2 implementation
branch; do not assume it already exists on `origin/main`.

### View contents

Summary cards show total registered receivers, enabled receivers, disabled
receivers, and invalidated/expired receivers. Also show the count of accounts
with an enabled receiver. Totals use the selected filters and the entire result
set, not the current page. If a region is unavailable, explicitly mark totals
partial/stale instead of interpreting missing values as zero.

The list contains one row per receiver, including disabled and invalidated
records, with these metadata fields:

| Field | Meaning |
|---|---|
| Customer | Authorized customer email/display label and regional user ID |
| Region | Owning Manager region; retained on every row |
| Receiver | Opaque receiver ID, reported device label, channel, revision |
| Subscription status | Registered/enabled, disabled, revoked, expired, or invalidated |
| Permission report | Last client-reported granted/denied/default/unsupported value, otherwise unknown; report timestamp |
| Registration activity | Created time and most recent successful registration/reconciliation time |
| Latest delivery | Pending, provider accepted, failed, expired, or suppressed; unknown/no attempts when absent |
| Latest delivery detail | Attempt timestamp/count, provider HTTP status, sanitized error category, and next retry when pending |

Subscription state, client permission report, and delivery result are separate
columns. `Enabled` means registered and enabled in Pax, not a live device
connection or verified current OS permission. The server cannot continuously
observe browser/OS notification settings; preserve the last report's age and
never guess an online/offline state. Delivery 2 displays `No delivery attempts`
until delivery 3 has persisted real outcomes.

Support customer search, region/channel/subscription-status filters, bounded
server-side pagination, manual refresh, and a read-only receiver detail panel.
Regional pagination/cursors stay separate; an unavailable region is not a
complete empty page. No endpoint URLs, key material, raw provider response, or
message content appears in list/detail responses or exports. The initial
dashboard is read-only and does not provide a send-to-all action.

Customer details link to the filtered dashboard, whose cards show the scoped
subscription counts.
Preserve the canonical `(region, user_id)` references before the existing
customer-by-email merge. A merged cross-region customer may have multiple
references; do not query only the first row's user ID or merge receiver IDs
across regions. Add return navigation from the receiver's customer identity.

### Regional read path and refresh

Use the existing same-origin `/api/pax` path. The new admin-only read endpoint
is intercepted by the same regional aggregation layer as customer analytics;
it forwards the original identity to fixed regional Managers without changing
account routing, provisioning accounts, or elevating normal users with a service
credential. Reuse the currently active identity/origin-assertion scheme rather
than introducing an Access-only variant.

Each regional response includes availability, server snapshot time, filtered
status totals, receiver metadata, and pagination. Authorize before cache reads.
A 401/403 from either region fails the aggregate and clears displayed protected
data. Transient regional errors may retain a clearly marked prior snapshot.

Reuse `useFocusedQuery` behavior: refresh every 15 seconds only while visible,
focused, and online; no overlapping requests, hidden polling, or catch-up burst.
Scope query keys by administrator identity and filters, abort on filter change,
and prevent stale results from a previous customer/filter replacing the current
view. Paginated rows and summary counts must use matching filter semantics.

### Dashboard acceptance

- Administrator sees all subscription states and can filter any customer/region.
- Non-admin and preview-as-user cannot read the dashboard; server tests assert
  denial occurs before store access. Credentials never appear in response DTOs.
- Receiver rows and summary counts match, including users with multiple devices
  and the same email represented by distinct regional user references.
- Permission changes not yet reported remain explicitly unknown/stale, and
  accepted delivery is never labeled displayed/read.
- Region outage, empty registry, backend-not-configured, access denial, and
  actual zero results have distinct UI states. Hidden/blurred tabs do not poll.
- Read-only registry/API and dashboard ship together in delivery 2; delivery 3
  adds latest attempt metrics without changing subscription-state semantics.
- New backend read-model/handler code meets the 80% unit coverage requirement;
  frontend tests cover permissions, navigation, filters, partial snapshots, and
  focus-based refresh.

## Completion and delivery flow

1. Authenticate and correlate a terminal prompt response to its canonical
   business turn and durable owner. Confirm normal completion; a permission
   response, stream EOF, timeout, cancellation, idle snapshot, or transport ACK
   is not a success event.
2. Flush preceding history. Atomically commit the terminal history marker, the
   unique notification, and tasks for receivers enabled at completion. Receivers
   registered later do not receive past events. Test rollback and replay.
3. A bounded worker claims due tasks using `FOR UPDATE SKIP LOCKED`, short
   transactions, expiring leases, and a lease token. Network calls happen after
   transaction commit. Multiple Manager replicas can run workers safely.
4. Reload receiver and current session authorization immediately before sending.
   Owner, revision, enabled flag, and expiration must still match the task.
   Disabling a receiver also suppresses its unclaimed pending tasks. The small
   race with a network call already in flight cannot be eliminated.
5. The Web Push adapter encrypts with subscription keys and signs using VAPID.
   POST to the subscription endpoint over HTTPS. Bound request timeout and send
   the remaining TTL so provider queuing cannot extend notification lifetime.
6. Record provider acceptance, terminal failure, expiration, or next retry.
   An expired lease can be reclaimed; updates require the lease token.
7. Service worker displays the generic notification using the stable ID as
   `tag` and `renotify: false`. A click focuses/navigates an existing Console
   window or opens the validated same-origin session link. Session authorization
   runs again after navigation; no credentials are placed in the push payload.

An uncertain send can be retried after the provider already accepted it. The
contract is durable, bounded retry with idempotent tasks and notification tags,
not exactly-once display. Provider acceptance is not a device-display or read
receipt. Do not label it `delivered` in UI or metrics.

### Web Push result policy

- 2xx: accepted by provider; terminal success.
- 404/410: receiver gone; conditionally invalidate that subscription revision.
- Transport uncertainty, 408, 425, 429, and 5xx: retry.
- Other failures, including 400/401/403/413: terminal failure; preserve receiver.
  Authentication/configuration errors need an operator signal, not mass receiver
  deletion.
- Six attempts maximum. Backoff after failures 1-5 is 10/20/40/80/160 seconds;
  respect a longer `Retry-After` without scheduling beyond expiration. The worker
  may add bounded jitter. No infinite retries or browser-side polling.

The Web Push adapter must allow only configured provider hostnames, reject
redirects, validate resolved destinations and prevent private-network dialing.
Subscription shape validation alone is not outbound request authorization.

## Browser and deployment behavior

- Only the user's enable action can request permission. Unsupported, denied,
  pending, enabled, and registration-failed states have explicit UI feedback.
- Register a root-scoped service worker served with a JavaScript content type
  and revalidation/no-store behavior. It handles push and notification clicks;
  it does not intercept fetches or cache authenticated Console responses.
- Make registration independent of an active session component. After a valid
  login, reconcile existing subscriptions without re-prompting. Detect VAPID key
  mismatch and safely resubscribe; do not regenerate VAPID keys at each boot.
- Every accepted visible Web Push results in a notification. Background-only
  behavior needs a separate presence design and is not part of this release.
- Worker code must not require a current Cloudflare Access cookie to display a
  push. A click may need login again. Verify the worker script is not redirected
  to an Access HTML login page during installation or update.
- Keep VAPID private material in server deployment secrets. Expose only public
  key/key ID to the Console. Configure the public Console origin and allowed
  push hosts per environment; do not derive external links from request headers.
- The Android TWA already enables notification delegation. Validate Android
  notification permission and trusted-site association on an installed build.
- Adding original native FCM/APNs clients later requires channel-specific device
  registration and adapters; no change to the business completion API.

## Incremental delivery and acceptance gates

| Delivery | Scope | Acceptance |
|---|---|---|
| 1. Core rules | Value types, subscription validation, minimal payload, stable IDs, fanout/owner/revision checks, retry classification | BDD-style unit tests; >=80% new backend coverage; no network activity |
| 2. Durable receiver registry and admin dashboard | Schema, memory/Postgres stores, authenticated API, limits/revoke/account switch, VAPID config, regional admin read model, Push subscriptions beside Customers | Owner isolation, idempotency, conflicting owner, key rotation, migration/API generation, admin denial, regional status totals, dashboard filters and navigation |
| 3. Real Web Push test | Outbox/leases, worker, VAPID adapter, Console enable/disable/test UI, service worker, latest delivery results in admin dashboard | A test push reaches an installed TWA with its page closed; denial/retry/410/SSRF paths and accurate provider-accepted labels tested |
| 4. Turn integration | Reliable normal-completion projection and atomic outbox/fanout; disconnect/replay coverage | Completing a real turn sends once per eligible device across browser disconnect and Manager restart |
| 5. Release validation | Multi-replica recovery, metrics/retention, docs and rollout | Real device test, click routing, logout/disable, retry/expiry and rollback verified |

Keep automatic turn notifications disabled until deliveries 2-4 are complete
and configured. Explicit device opt-in and manual test pushes are available in
delivery 3 when both VAPID keys and the contact are configured. The public config
reports `turn_completion_enabled` once the standard-session completion path and
sender are available. Console explicitly identifies encrypted sessions as
unsupported; this flag does not claim encrypted-session completion support.
Release backend before Console; incomplete/disabled backend config renders the
UI unavailable without permission prompts. Disabling the feature stops task
creation and sending. Expired jobs must not be replayed on re-enable.

Delivery 4 must cover background and direct-tunnel completion as well as the
normal `/conversation` stream. First prove the history/completion path survives
browser disconnect. If current `turn_done` persistence is stream-dependent,
move completion projection to the correlated durable inbound consumer rather
than adding a push call to an HTTP writer.

E2EE is an explicit release gate: either implement an authenticated, durable,
minimal paxd terminal signal (canonical scope, business turn, terminal outcome,
no ACP plaintext) using the reliable node transport, or clearly expose that
encrypted sessions are unsupported. Never decode encrypted history on Manager
or infer successful completion from missing active turns. This requires a
separate paxd protocol review before its implementation.

## Design review

Reviewed against the current sources on 2026-10-19. No separate agent review was
performed. The following findings are incorporated, not deferred assumptions:

| Finding | Decision |
|---|---|
| Browser lifetime cannot govern completion | Durable correlated terminal event owns creation |
| Replays and replica races can duplicate work | Stable IDs, SQL uniqueness, leases, conditional result updates |
| Atomic history plus outbox is required | One transaction; test crash/rollback; no send in the SSE writer |
| Receiver ownership can change while jobs wait | Auth-derived owner, immutable binding, revision checks, revoke before logout |
| Provider API semantics differ | Canonical outcomes; Web Push HTTP policy stays in its adapter |
| Provider accepted is not user notified | Explicit accepted state, TTL, and bounded retry |
| A subscription can target an attacker-controlled URL | Provider host policy and checked outbound dialing before real sending |
| E2EE hides completion from Manager | Separate minimal terminal signal or explicit release limitation |
| Root service worker shares auth/caching boundaries | Push-only worker, no fetch interception, verify Access script delivery |
| Dashboard must match existing Customers administration | Adjacent admin entry and page switcher; reuse original-identity regional reads |
| Registered receiver does not prove current device permission or delivery | Separate subscription state, timestamped client report, and provider outcome |
| Existing Customers merges email across regions | Retain regional user references and never merge receiver identity by email |

Deliveries 1-2 are merged. Delivery 3 adds the durable test outbox and worker,
VAPID sender with provider/dial restrictions, device opt-in controls, a push-only
service worker, logout/reconciliation handling and actual attempt metadata.
Browser QA uses explicit fictional permission/subscription fixtures. The owner
confirmed real test notifications arrive. Delivery 4 now commits correlated
standard-session terminal history, the completion ledger and receiver fanout in
one transaction. Reliable storage failures retry independently of browser sockets;
original inbox timestamps bound TTL and late registration. Sending rechecks current
session ownership. E2EE terminal signaling remains unsupported and visible in the
Console; no encrypted payload is inspected by Manager. Real-turn OS display after
deployment is still owner acceptance. A pending task with zero attempts is not
shown as attempted.

## Sources

- [Google: how Web Push works](https://web.dev/articles/push-notifications-how-push-works)
- [Google: subscribe a user](https://web.dev/articles/push-notifications-subscribing-a-user)
- [Chromium: TWA permission delegation](https://chromium.googlesource.com/chromium/src/+/HEAD/chrome/android/java/src/org/chromium/chrome/browser/browserservices/permissiondelegation/README.md)
- Installed Next.js guide: `pax-console/node_modules/next/dist/docs/01-app/02-guides/progressive-web-apps.md`.

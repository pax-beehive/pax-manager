# Regional user directory (KEV-75)

This Worker verifies Cloudflare Access identity, assigns one immutable region in
D1, then provisions the same global user ID in that region's Manager. It never
connects directly to regional PostgreSQL databases.

## Contract

`POST /api/v1/region/bootstrap`, with `Content-Type: application/json` and a
verified `Cf-Access-Jwt-Assertion`:

- `{}` resolves an existing account, or returns
  `{"status":"selection_required","regions":["us","hk"]}` without creating one.
- `{"preferred_region":"hk"}` assigns a new account, or returns its existing
  assignment even if the preference differs.
- Success: `{"status":"ready","user_id":"usr_...","region":"hk","api_url":"https://..."}`.
- Invalid identity: 401. Invalid input or cross-origin browser request: 400.
  Directory/provisioning failure: 503, with `retryable: true` after lookup begins.
  Configuration failure: 503. Failure never defaults to US or changes regions.

All responses are `no-store`. Identity/email/user ID cannot be supplied in the
body. The Worker verifies the JWT signature, trusted issuer, audience and expiry.
`ACCESS_PROVIDERS` is a JSON array of `{ "issuer": "https://TEAM.cloudflareaccess.com",
"audience": "ACCESS_APPLICATION_AUD" }`; multiple entries support an explicitly
configured Access migration. Only verified, normalized email identifies a user.
Email changes require manual reconciliation; there is no automatic account merge.

D1's unique identity constraint chooses the winner during concurrent signups.
Reads use Sessions with the last signed bookmark, or `first-unconstrained`.
Replica misses are confirmed on `first-primary`. Allocation is a primary-session
atomic batch, returning its committed winning row and bookmark. A failed regional
provision leaves that assignment intact; retry always reaches the same Manager.

After provisioning succeeds, `__Host-pax_region` is an identity-bound, signed,
Secure/HttpOnly/SameSite=Lax cookie. Its assignment shortcut lasts **10 seconds**
and is not extended on cache hits. Its bookmark lasts one day. After 10 seconds,
lookup uses the bookmark so replicas must satisfy read-your-writes; the TTL alone
is not a replication guarantee. Missing/invalid cookies fall back to D1. If the
cookie expires or the client changes devices, immutable assignments plus primary
confirmation of misses preserve correctness. The cookie is not a Manager login
credential and is not forwarded to regional domains.

## Manager boundary

Set `PAX_REGION=us` or `hk` and `REGION_PROVISIONING_SECRET` (at least 32 bytes).
Partial configuration fails startup validation. With these unset, legacy
single-region behavior is unchanged and `/internal/users/ensure` returns 404.

In regional mode, both ordinary authenticated requests and the static
registration-owner path only read existing users. Missing users are unauthorized.
API keys and persisted registration tokens retain their existing behavior.
Admin permissions remain Manager-controlled.

`POST /internal/users/ensure` accepts exactly:

```json
{ "user_id": "usr_...", "identity_key": "owner@example.com", "region": "hk" }
```

`X-Pax-Timestamp` is Unix seconds. `X-Pax-Signature` is lowercase hex HMAC-SHA256
using the destination region's secret, over the exact UTF-8 bytes:

```text
pax-region-ensure-v1\n<TIMESTAMP>\n<REQUEST_BODY>
```

Manager checks the signature, destination region, normalized identity and a
30-second age limit (up to 5 seconds clock skew into the future). The body is
limited to 2048 bytes. Replays are idempotent. Email/ID conflicts return 409 without
mutating the existing account. The Worker currently surfaces this as retryable
503; an operator must reconcile such conflicts before retry can succeed. There
is no distributed transaction: D1 commits first, and local provisioning can be
retried. User deletion and region migration are outside this contract.

## Setup and controlled activation

`wrangler.jsonc` is the local template. `wrangler.production.jsonc` targets the PAX
account, its dedicated directory database and only the unified bootstrap route.
Both default `BOOTSTRAP_ENABLED` to `false`: staged deployments return an explicit
503 without authentication, D1 queries, allocation or Manager provisioning. Only
the literal value `true` enables bootstrap. Do not enable it until the client,
regional Managers and final inventory import are ready.

1. Verify the production D1 binding ID in Wrangler. Configure the real
   unified origin, US/HK Manager origins and trusted Access provider(s).
2. Apply `npx wrangler d1 migrations apply pax-user-regions --remote --config wrangler.production.jsonc`.
   Enable D1 read replication in the database settings when desired; the code
   already uses Sessions. Local Miniflare tests exercise SQLite/D1 transactions,
   not Cloudflare's replication lag; a separate session test simulates stale misses.
3. Set three distinct random secrets via `npx wrangler secret put NAME --config wrangler.production.jsonc`:
   `DIRECTORY_SECRET`, `US_PROVISIONING_SECRET`, `HK_PROVISIONING_SECRET`.
   Each regional Manager receives only its matching provisioning secret.
4. If the Manager origin is behind Access, configure region-specific Access
   service credentials (`US_ACCESS_CLIENT_ID`, `US_ACCESS_CLIENT_SECRET`, and HK
   equivalents) as Worker secrets, with an Access service-auth policy admitting
   them to the internal route. Do not bypass Access for browser APIs. The HMAC
   check is mandatory even when the network/Access policy already permits access.
   Keep machine-only origins denying `/internal/*` unless specifically needed.
5. Deploy Manager code with regional mode still disabled. Stage the Worker and
   Access-protected unified bootstrap route. A staging request must not allocate
   production identities before the inventory import is complete.
6. During a coordinated signup pause, export both regional inventories, review
   automatic duplicate decisions, apply the validated import, enable regional mode on both Managers,
   set `BOOTSTRAP_ENABLED=true`, and activate the unified bootstrap entrypoint. Keep signup paused across the
   export/import/configuration boundary so legacy implicit creation cannot race
   the directory. Existing users remain readable throughout Manager rollout.
7. Verify existing US users keep their IDs, a new HK account only exists in HK,
   concurrent conflicting preferences resolve one way, and retries after a
   provision failure keep the same ID. Verify short cache expiry with real D1
   replicas. End the signup pause only after these checks.

Full proxy routing, Console selection, installer auto-selection and paxd credential
changes belong to KEV-76/77/78. This change supplies the bootstrap contract; it does
not activate those client integrations. Do not enable regional mode for new users
before a bootstrap-capable entrypoint is available.

After activation, a rollback must preserve D1 mappings and keep regional Managers
in read-only identity mode. Re-enabling independent implicit creation on both
regions would break global uniqueness. Secret rotation invalidates old assertions;
cookie-secret rotation safely falls back to D1 lookups.

## Existing-user import

Export only `user_id` and `email` from each regional PostgreSQL database:

```sql
SELECT COALESCE(json_agg(json_build_object('user_id', user_id, 'email', email)), '[]')
FROM users;
```

Store exports outside source control with owner-only permissions. Then:

```sh
node scripts/import.mjs import-us.json import-hk.json import-reviewed.sql
# Review the generated SQL privately, then apply explicitly:
npx wrangler d1 execute pax-user-regions --remote --config wrangler.production.jsonc --file import-reviewed.sql
```

The script is a dry-run: it writes new owner-only files (mode 0600), never alters
a database and never overwrites an output. Existing regional IDs are preserved.
The precedence is **existing D1 assignment, then existing US account, then HK-only
account**. When the same normalized identity exists in both regional inventories,
the US ID is selected automatically. Users are not asked to resolve the duplicate.
The HK account and all of its business data are left untouched; this is routing
selection, not data migration or an account merge.

`OUTPUT.sql.decisions.json` records the inventory's preferred account and the other
regional account for operators. These are import candidates, not assertions that
the directory was changed. The generated SQL uses `ON CONFLICT(identity_key) DO
NOTHING`, so an already established D1 assignment takes precedence even over a US
candidate. Re-running an import cannot switch an existing user's region or ID.

Corrupt source inventories (two different IDs for one normalized identity within
one region, or one ID reused for different identities) still produce a private
`OUTPUT.sql.conflicts.json` report and no SQL. These are operator preflight failures,
not browser errors. A global ID already used by another identity in D1 aborts the
single SQL statement atomically; no partial import is committed. The immutable
trigger still prevents updates to established identities and regions.

Inputs larger than a 90KB statement require a separately reviewed staged import.
Do not delete or rename HK accounts merely to suppress a duplicate.

## Development

Node 24 or later:

```sh
npm ci
npm run format:check
npm run typecheck
npm test
npm run build
```

`npm test` uses local Miniflare D1 and enforces 80% for statements, branches,
functions and lines across Worker source. Go tests cover HMAC verification,
read-only auth, idempotent memory/SQL provisioning and the HTTP boundary.
The optional real PostgreSQL concurrency test runs with:

```sh
PAX_MANAGER_REGION_TEST_DATABASE_URL=postgres://... \
  go test -race ./internal/manager/storage -run TestRegionalUserGivenConcurrentPostgres
```

Stable Miniflare 4 is used with patched `undici` and `sharp` overrides. These are
test-only dependencies; Wrangler uses its own runtime. `npm audit` is clean with
the checked-in lockfile.

## Browser routing activation (KEV-76 / KEV-77)

`wrangler.browser.jsonc` is the explicit active browser deployment. Do not use
it until both Managers have regional provisioning enabled and legacy identities
have been reconciled while implicit signup is stopped. The staged production
configuration remains available for initial installation, not post-activation
rollback: removing active routes would restore the old single-region proxy.

The browser flow runs before Console `/me`: POST bootstrap with `{}` restores
an existing account; new identities receive `selection_required`. Two uncached
same-origin `/api/v1/region/probe/us|hk?nonce=...` calls per region measure a real
Manager `/health` fetch through the selected origin. Probe responses echo the
nonce. A failed origin is not recommended or selected by default.

`__Host-pax_route` is an HttpOnly, Secure, SameSite=Lax HS256 credential with
issuer `pax-region-directory`, audience `browser-route`, protocol version 1,
verified email subject, immutable user ID/region, iat/exp, and signing `kid`.
It expires after one hour. The active key is `DIRECTORY_SECRET` with
`ROUTING_KEY_ID`; optionally retain `PREVIOUS_DIRECTORY_SECRET` and
`PREVIOUS_ROUTING_KEY_ID` during a rotation window. Remove the previous key
for immediate credential invalidation. Access identity is verified on every
request; a route credential never replaces authentication. Logout invalidates
Access and the route credential cannot authenticate by itself. Bootstrap refreshes
before expiry, and missing/invalid/expired credentials recover only an existing
D1 assignment. A failed lookup never allocates or chooses a default region.

The Worker intercepts `/api/pax/*` and browser `/api/v1/user/*` WebSockets.
Only user-scoped endpoints, health, and the public paxd download resolver are
allowed. It forwards to the fixed Manager origin selected by the credential or
D1, preserves request/response streams, and returns WebSocket upgrades directly.
It never retries a business mutation or falls back to another region. Untrusted
Authorization, service tokens, local-user headers and regional hints are not
forwarded. The verified Access JWT is forwarded in the origin assertion and
CF_Authorization cookie. This deployment requires both origins to share the
same Access application trust; optional service authentication is still supported
for provisioning. No new Access policy bypass is needed for the current origins.

The origin and response are no-store; origin cookies and cross-origin allow
headers are stripped. Redirects are rejected except Manager-issued HTTPS
content-download redirects, which are returned to the browser without forwarding
credentials to storage. No upstream redirect is followed. Workerd supports
`redirect: manual` (not `error`); `cache: no-store` must not be combined with
`cf.cacheTtl`. The runtime integration test exercises the bundled Worker with
real RSA identity verification, D1, HK provisioning, SSE and WebSocket frames.

Console uses runtime `PAX_BROWSER_REGIONS_ENABLED=true` and
`PAX_REGION_PUBLIC_ORIGIN=https://paxworkspace.net`. Alternate Console hostnames
navigate to the canonical host. HTML/static assets remain on the common Console;
business traffic is regional. The Next REST fallback returns 503 when this mode
is enabled, so a missing Worker route cannot send requests to the old US default.

## Paired machine routing (staged)

Machine requests authenticate with the existing Node Key. `X-Pax-User-ID` is a
disposable routing hint, never an authorization input. No machine ticket, ticket
signing key, expiration or refresh protocol is needed. Browser cookies retain
their separate signed protocol above.

The Worker uses the hint's D1 record only to order discovery. It calls read-only
`GET /api/v1/node/identity` on fixed regional machine origins with the Node Key.
The Manager returns the authenticated node's actual `node_id`, `user_id` and
`region`; the Worker checks that owner's D1 assignment before forwarding the
original business body exactly once. Missing, corrupt and other-user hints are
repaired. An unavailable unrelated origin does not block a valid key accepted by
the other origin. Failed business requests are never replayed in another region.

HTTP and WebSocket responses include the corrected `X-Pax-User-ID`. paxd stores
it as a best-effort cache scoped to origin and Node Key, learns only from 2xx or
101 responses, and preserves its cache during failures. D1 remains authoritative
for user-to-region assignment; regional Manager authentication remains
authoritative for Node Key ownership and permissions. Discovery never creates a
user or changes an assignment. An absent D1 record returns 503; an inconsistent
actual-owner assignment returns 409 and needs operator reconciliation.

Each handshake/request currently performs identity discovery and D1 lookup;
there is no server-side authenticated-identity cache. Discovery uses a five-second
timeout per region. HTTP bodies, SSE and WebSocket upgrades are streamed. Paths
and methods come from the reviewed Manager node policy manifest. Only header
Node Keys are supported; query credentials, anonymous pairing, registration-token
onboarding, legacy agent-only keys and unknown paths are excluded.

Machine routing is disabled unless all four variables are explicitly supplied:

```text
MACHINE_ROUTING_ENABLED=true
MACHINE_PUBLIC_ORIGIN=https://machine.example.com
US_MACHINE_URL=https://us-origin.example.com
HK_MACHINE_URL=https://hk-origin.example.com
```

The two upstreams must be distinct HTTPS origins, reachable without interactive
Access login, and must not route back to this Worker. These example values are
placeholders, not a deployable production config. Before activation, deploy the
Manager endpoint in both regions, verify the origin/Access path policy, complete
the unified pre-pairing flow, and test both old and new paxd clients. Preserve all
active browser routes when adding machine routes. Do not deploy the disabled
initial `wrangler.production.jsonc` over an active deployment.

Existing regional machine origins and APIs are not intercepted by the active
browser config. Unified pre-pairing, installer integration and production machine
activation remain KEV-76/78/77 follow-up scope. Keep these issues open.

## PAX Release integration

Production uses `wrangler.browser.jsonc`. `npm run deploy` now explicitly selects
that file and retains remote variables; the old `wrangler.production.jsonc` is
not a production rollback configuration. Ordinary CI uses `versions upload`, not
`deploy`: uploading never activates traffic, modifies routes or migrates D1.

`ci_publish.py upload` captures Wrangler output privately, associates the version
with a full commit annotation, and compares all bindings/runtime settings with
the live version before writing `release-artifact/worker.json`. Only the initial
`WORKER_VERSION` metadata binding addition is allowed. Existing secret bindings
are retained. `--keep-vars` does not by itself preserve other binding types; the
explicit post-upload comparison rejects configuration drift before registration.
A rejected uploaded version remains inactive. Secrets are never in the manifest.
Cloudflare also rejects deploying old versions with incompatible secrets; the
Release controller never uses `force` to bypass this protection.

`ci_publish.py register` registers that immutable manifest using the existing
publisher token and optional paired Cloudflare Access credentials. The workflow
uploads the manifest as a GitHub artifact first so registration can be retried
without rebuilding. Run both scripts from this directory. The checked-in JSONC
configuration currently uses trailing commas and no comments; the CI reader
supports this restricted form and fails closed on unsupported syntax.

Enable the workflow only after both Managers and the Release controller support
this contract:

- Deploy Managers to US/HK first. `/health` now includes `region` and
  `region_directory_capabilities` (provision-v1, browser-v1, paxl-login-v1,
  customer-analytics-v1). This is additive and contains no user data.
- Add a dedicated random `RELEASE_PROBE_TOKEN` Worker secret (at least 32 chars)
  and store it in the controller's private probe-token file. Preserve all current
  secrets, database bindings and routes during this one-time setup.
- Ensure existing `US_ACCESS_CLIENT_ID/SECRET` and `HK_ACCESS_CLIENT_ID/SECRET`
  bindings permit Worker health requests to the corresponding Managers. The
  controller needs equivalent Access access for direct preflight checks.
- Allow the controller's Access service identity on the diagnostic path
  `/api/v1/region/release-health/*`. Keep ordinary browser/user policies intact.
- Set repo secret `PAX_WORKER_CF_API_TOKEN` to a dedicated account-scoped Workers
  Scripts Write token. No D1 or route write permission is required. Reuse the
  existing PAX_RELEASE publisher and Access secrets for registration.
- Set repo variable `PAX_WORKER_RELEASE_ENABLED=true`. Verified main CI uploads
  versions automatically; the `Upload region directory Worker` workflow also
  supports manual main reruns. Until enabled, image CI is unaffected.

The diagnostic GET endpoint requires its dedicated bearer token and returns only
Worker version metadata and one regional Manager's health/capabilities. It never
creates users or accesses D1. A browser/Access token alone does not authorize it.
Release checks US and HK before activation and verifies both through the Worker
after activation. These probes verify version, protocol and connectivity, not a
full browser session or every routing operation.

Historical versions can be selected and republished using the same flow; there
is no automatic rollback or D1 restore. Old versions without the diagnostic
contract or matching configuration require a separately reviewed manual recovery.
Staging and the existing machine-routing follow-up are still open.

# Hosting machine and authenticated owner ingress

The existing `pax-region-directory` browser Worker also supplies a narrow
service-bearer ingress. `wrangler.browser.jsonc` adds only
`paxworkspace.net/api/v1/hosting/*`; all prior browser/native routes remain.
There is no new Worker or secret binding. This implementation is not evidence
of a production deployment.

## Service boundary

The service ingress accepts exactly these POST paths, without a query string:

```text
/api/v1/hosting/gateway/accounting
/api/v1/hosting/jobs/claim
/api/v1/hosting/jobs/heartbeat
/api/v1/hosting/jobs/complete
/api/v1/hosting/jobs/enroll
```

`HOSTING_SERVICE_REGION=us` explicitly enables one authoritative US ledger.
Unset, empty, `hk` or any other value leaves these paths unavailable. The origin
is the existing HTTPS `US_MANAGER_URL`; the public hostname is `PUBLIC_ORIGIN`.
The existing `US_ACCESS_CLIENT_ID` and `US_ACCESS_CLIENT_SECRET` bindings admit
the fixed origin hop. There is no HK fallback, directory lookup, region hint,
provisioning, browser authentication, or synthesized user assertion in this
service branch.

The gateway's or trusted host's own bounded Bearer token crosses unchanged in
meaning. Manager still validates its independent gateway key for accounting
and host key for jobs before service operations. The Worker stores neither
application key and does not promote user, model, node or wrong-role tokens.
Manager hosting and quota policies remain authoritative.

Enrollment additionally requires the registered node's `X-Pax-Key`. The
Worker forwards this header only for the exact enrollment POST. Manager
validates the node key alongside the host Bearer token before enrolling the
node. The other service and owner routes discard the node key.

The origin request is built from a fresh header allowlist. Browser cookies,
user-email overrides, user/node identifiers, identity assertions, incoming CF
client credentials and Access assertions are discarded. Service requests enter
before WorkOS browser login, while ordinary browser/native APIs retain their
existing authentication. Failed service requests do not fall through to a user
or node route.

Input is limited to64KiB and valid JSON responses to2MiB. A single15-second
deadline covers incoming body reads, the fixed origin fetch and response reads.
Redirects are rejected; requests are never retried. Responses are `no-store`
and omit cookies, CORS grants and arbitrary origin headers. Only numeric
`Retry-After` is retained. HTML, malformed JSON, oversized replies and transport
failures become fixed safe errors without logging upstream bodies or keys.

## Authenticated owner operations

The existing owner handlers are now allowed only for:

```text
GET  /api/v1/starter
POST /api/v1/starter
POST /api/v1/starter/wake
POST /api/v1/starter/revoke
```

Browser callers use the existing `/api/pax` prefix, for example
`/api/pax/api/v1/starter/wake`, with their real WorkOS session and same-origin
checks. Native WorkOS callers use `/api/v1/native/client/starter` and
`/api/v1/native/client/starter/wake`. Existing native API-key callers can use
`/api/v1/native/us/starter` and its wake suffix with their real `paxu_` key.
The revoke suffix uses the same owner authentication and takes the exact
`node_id` and `agent_id` in the request body.

Those existing route prefixes are sufficient; no direct Starter CF route was
added. Wrong methods, trailing slashes and neighboring paths are rejected.

The browser sandbox controls use the same `/api/pax` prefix, WorkOS identity,
origin check and home-region routing. Exact allowed methods and paths are:

```text
GET  /api/v1/sandbox
POST /api/v1/sandbox
POST /api/v1/sandbox/resume
POST /api/v1/sandbox/restart
```

Manager determines availability for the current account. Sandbox controls are
browser-only. Recovery targets the registered Node ID, and restart also checks
the last observed sandbox generation so retries cannot restart a later run.

The browser prefix is removed before origin signing, and the original Manager
method/path is signed as before. Manager aliases, signature formats and owner
resolution are unchanged. Manager derives the owner from the authenticated
principal. Caller-chosen user IDs and production local-user headers cannot
replace this flow.

WorkOS owner requests still follow their immutable D1 home region. A US-only
service ingress cannot execute a lease created on the independent HK ledger.
Commission a real owner whose authoritative account and hosting lease belong
to the selected US ledger; do not change or invent identities to hide a region
mismatch.

## Review and explicit deployment

The checked-in browser config leaves `HOSTING_SERVICE_REGION` empty. Deploying
that configuration alone leaves service hosting disabled. After authorized
Manager setup, review the existing Worker/account/zone/routes and current US
Access binding names without printing secret values. The existing US origin
credentials are reused; no separate API service token needs to be requested.

From this directory with Node24:

```sh
npm ci --ignore-scripts --no-audit --no-fund
npm run typecheck
npm test
npx wrangler deploy --dry-run --config wrangler.browser.jsonc --outdir dist/browser
```

After operator authorization, compare the live Worker version, binding names
and nonsecret runtime/resource settings. Preserve all five existing routes;
add only the hosting prefix. Keep the existing D1 ID, rate-limit namespaces,
version metadata binding, compatibility date and flags. No new resource,
secret, Access policy or D1 migration is needed by this patch.

`--keep-vars` retains remote plain-text/JSON variables that are absent from the
deployment config; variables explicitly declared in a config still replace
their live values. Wrangler deploy also inherits unchanged secret bindings.
If every existing config variable has not been compared with live state, use
an operator-private deployment config containing only the new hosting variable
and the reviewed full resource/runtime/route configuration. Use absolute local
source paths if that config is stored outside this directory. Do not copy
secret values into that file. Let `PAX_HOSTING_DEPLOY_CONFIG` identify it.

With Node24 and the installed pinned Wrangler, the explicit release commands
are:

```sh
npx --no-install wrangler deploy --dry-run --config "$PAX_HOSTING_DEPLOY_CONFIG" --keep-vars --var HOSTING_SERVICE_REGION:us
npx --no-install wrangler deploy --config "$PAX_HOSTING_DEPLOY_CONFIG" --keep-vars --var HOSTING_SERVICE_REGION:us
```

The CLI override enables US in the deployed version without changing the
source's disabled default. It persists in that version until explicitly
changed; a shell variable alone does not configure the Worker. Do not use
`--env us`, which selects a Wrangler deployment environment instead of the
hosting ledger. These commands are an operator handoff, not commands executed
by this change.

Keep `wrangler.browser.jsonc` comment-free: `ci_publish.py` accepts trailing
commas but uses the JSON parser without comment handling. Release version
upload also compares all binding configuration with the live version. While
live hosting is `us` and checked-in hosting is empty, an ordinary CI upload is
expected to fail that drift check and remain inactive. The explicit operator
deployment does not automatically resolve this difference. Review a separate
release configuration change before resuming automated publication; do not
weaken the comparison or force registration. Do not use the initial bootstrap
config to replace the active browser configuration.

Gateway accounting uses `GATEWAY_MANAGER_URL=https://paxworkspace.net` and its
own Manager key. Trusted hosts use that same public origin and their host key.
The CF origin pair stays inside this Worker; tenants do not receive it. Root
operator work still supplies Manager environment/policy staging and a real
authenticated owner lease before end-to-end commissioning.

Missing, malformed, wrong-role and tenant credentials must fail without ledger
mutations. Confirm authenticated owner admission, exact region, job claim,
heartbeat and completion against the real staged Manager before enabling paid
generation. Unit/runtime tests use only mocks and no paid provider calls.
To pause ingress, use the same protected config and explicitly override the
region to an empty string. Other live variables and all six routes remain:

```sh
npx --no-install wrangler deploy --config "$PAX_HOSTING_DEPLOY_CONFIG" --keep-vars --var 'HOSTING_SERVICE_REGION:'
```

For a full code rollback, use the saved pre-release100% version ID rather than
the automatically selected previous version:

```sh
npx --no-install wrangler rollback "$PAX_HOSTING_PREVIOUS_VERSION" --config "$PAX_HOSTING_DEPLOY_CONFIG" --message hosting-ingress-rollback
```

A version rollback does not undo bound resources or route changes. Restore the
saved route inventory separately only if necessary, removing only the added
hosting route. Preserve all original browser routes and secret bindings; never
force an incompatible-secret rollback. Reconcile existing Manager reservations;
pausing or rollback does not refund or delete them.

# Public Node API rollout (KEV-35)

## Status

Code review baseline: 2026-09-09. The current client requires 28 reviewed machine
routes, including the ACP WebSocket at `/api/v1/agent/tunnel`.

Production was verified over SSH on `home_dev` (Ubuntu) on 2026-09-09.
Read `/home/congkai/pax_workspace/AGENTS.md` and `DEPLOYMENT_RUNBOOK.md` on that
host before operations. The tunnel is remotely managed and runs as the
`pax-cloudflared` container on Docker network `pax-manager_default`:

| Hostname | Tunnel origin | Role |
|---|---|---|
| `ws.lakeward.net` | `http://pax-console:8080` | Console |
| `api.lakeward.net` | `http://pax-manager:9879` | Manager |
| `wsapi.lakeward.net` | `http://pax-manager:9879` | Manager alias |
| unmatched | `http_status:404` | Denied |

These mappings were checked against the latest ingress configuration in the
tunnel logs. At the initial audit the host checkout matched `e0814a28eb65`. On September
10 the checkout had advanced to `18e15ff` while the running image remained
`e0814a28eb65`. The isolated guard candidate uses the running image source
plus only the route guard and manifest. The application services and tunnel use `unless-stopped`.
Manager publishes only `127.0.0.1:9879`, Console `127.0.0.1:3000`, and Postgres
`127.0.0.1:5432`. Manager has both `CLOUDFLARE_ACCESS_DISABLED=false` and
`ALLOW_LOCAL_USER_HEADER=false`. Docker Server reports version 29.7.2.

Origin health returns 200. Origin mailbox, control tunnel, ACP tunnel, and
user `self/me` all return 401 without credentials. On both public Manager
hostnames, those paths and health return 302 to Cloudflare Access. This is the
confirmed immediate blocker for customer paxd without Access service tokens.
The complete credential-free probe passed all 31 cases against the actual
loopback origin: 28 reviewed routes and three negative path/method cases.
This verifies rejection behavior, not authorized flows or tenant isolation.
Earlier probes to `ws.lakeward.net` targeted the Console and should not be used
to diagnose Manager behavior.

GCP access is not needed. The checked-in VM pipeline and development Compose
file are not the running production configuration. No containers, secrets,
Access policies, or tunnel configuration were changed during this audit.

The machine-readable source is `internal/manager/nodepolicy/routes.json`.
[The route matrix](node_api_routes.md) lists method, authentication, ownership
boundary, rate class, and expected caller for every entry. Rate classes are
planning metadata, not new runtime limiters. Ownership descriptions identify
what KEV-40 must verify, not a completed isolation audit.

## Application enforcement

`Service.protect` rejects requests in `/api/v1/node` and `/api/v1/agent`
namespaces unless the resolved router template and HTTP method are in the
manifest. A newly registered handler in either namespace stays blocked until
the manifest is reviewed. Existing endpoint authentication remains in effect.
The route parity test detects missing, extra, and duplicate manifest entries.

The manifest is not a replacement for NodeAuth or resource ownership checks.
It also does not restrict non-machine namespaces or install an edge policy.
Unknown machine routes fail with 404. Canonicalization and redirects performed
by the HTTP router or edge still require production probes.

## Required edge policy

Apply these boundaries to the confirmed production hostname(s):

1. Keep the browser application, `/connect.html`, `/api/user/*`,
   `/api/v1/user/*`, and administration behind their existing user boundary.
   Keep `/openapi`, `/openapi.json`, `/api/echo`, debug, and unrelated APIs
   outside the machine bypass. An HTML shell or an OpenAPI document does not
   gain authorization merely because it is served by the same application.
2. Enforce the exact reviewed machine route shapes and methods in Manager;
   parameter placeholders represent one nonempty path segment. The concrete
   [Access change plan](../deploy/node-public/cloudflare-access-plan.json)
   proposes machine-path applications for `/api/v1/node/*` and
   `/api/v1/agent/tunnel` on each Manager hostname. Access's wildcard paths are
   coarser than the application guard, so deploy and verify the guard first.
   Bypass only removes the interactive login; Manager still requires the
   credentials in the matrix. Do not distribute a shared Cloudflare service
   token to customer nodes. Inspect overlapping Access apps before applying:
   more-specific apps override broader apps without inheriting their policies.
3. The bootstrap start route is anonymous but only creates a pending request.
   Poll requires its registration ID and poll token. Browser approval remains
   user-authenticated. Token-based registration requires an owner-bound
   registration token; agent registration also accepts an existing Node key.
4. Include `GET /api/v1/agent/tunnel`. A bypass for `/api/v1/node/*` alone breaks
   ACP. Both WebSockets authenticate before upgrade. Preserve upgrade headers,
   query parameters, and `X-Pax-Key`/Authorization headers.
5. Keep deployment health checks local or on a separate health-check path.
   Neither `/health` nor `/api/v1/health` is part of the customer Node API.
6. Configure installer/update downloads separately: the five GET routes
   `/api/v1/public/{artifacts/download,paxd/download,paxl/download,paxd/install.sh,paxl/install.sh}`
   are public distribution endpoints, not Node-authenticated APIs. Signed
   object-storage URLs also need non-interactive access on their own origin.
7. Deny unknown paths and unsupported methods on any dedicated machine
   hostname. On a shared hostname, retain interactive protection for all other
   paths rather than bypassing the whole host or all `/api/*`.

Legacy `/api/agent/register`, `/api/agent/status`, `/api/agent/mailbox`,
`/api/agent/sessions/:sessionId/mailbox`, `/api/agent/messages/offset`,
`/api/agent/messages/:messageId/result`, and `/api/agent/ws` remain registered
for compatibility. They are excluded from the current Node bypass. Older
clients need an explicit migration decision before opening those routes.
Paxl device-login routes are a separate CLI flow and also excluded.

## Origin and abuse controls still required

- KEV-36 applies to the home Ubuntu origin and Cloudflare Tunnel. Check the
  production tunnel reaches Manager over the shared private Docker network.
  Keep that origin URL and the existing loopback-only published ports.
  IPv4/IPv6 listener inspection showed only SSH listening on external host
  interfaces; application ports listen on IPv4 loopback. Host firewall status
  was not readable with the current noninteractive privileges, and router
  forwarding was not inspected. Do not claim a full network isolation audit.
- The root `docker-compose.yml` is for local development: it enables
  `CLOUDFLARE_ACCESS_DISABLED` and `ALLOW_LOCAL_USER_HEADER`, and publishes
  Manager, Postgres, and MinIO ports on host interfaces. Do not treat it as a
  production configuration without checking and overriding these settings.
- `clientAddress` currently trusts `CF-Connecting-IP` and `X-Forwarded-For`
  without checking the peer. Untrusted origin access could spoof rate-limit
  identity and onboarding network information. Fix trusted-proxy handling as
  part of KEV-36; an edge rule alone does not prove origin isolation.
- KEV-37: edge WAF and route-specific anonymous limits are not in this change.
- KEV-38: current rate buckets are in-memory and shared by client IP. There is
  no distributed per-node quota in this manifest.
- KEV-39: frame, connection, and rate bounds need a separate WebSocket review.
- KEV-40: existing tests cover several ownership boundaries, but every matrix
  row still needs malicious-node, payload-bound, and replay verification.

The host has a tunnel runtime token file, but no Access management token was
identified among the Cloudflare configuration file names. Tunnel credentials
are not used as Access-management credentials. Access-policy changes require
the owner's dashboard operation or a separately supplied management-token
file. Never put its value in chat, documentation, or a command-line argument.

## Verification and rollout

After confirming the SSH host, inventory it without dumping credentials:

```bash
ssh home_dev python3 - < deploy/node-public/audit_host.py
```

This reads listener addresses, selected container networking/mount paths,
Compose source paths, and the three port/local-auth environment flags. It does
not print process arguments, tunnel tokens, database URLs, or full environments.
Null fields can indicate missing tools or insufficient read permissions.

Run from the repository with Go 1.26 selected through gvm:

```bash
python3 deploy/node-public/check.py
GOCACHE=/tmp/pax-manager-go-cache go test ./internal/manager -run 'TestNodePublic|TestNodeRegistrationSessionConnectsNodeAfterUserApproval|TestRegisterNodeAgentWith' -count=1
make fmt-check
make lint
GOCACHE=/tmp/pax-manager-go-cache go test -count=1 ./...
GOCACHE=/tmp/pax-manager-go-cache go build -o /tmp/pax-manager-build-check ./cmd/manager
```

After applying the reviewed edge policy to the confirmed machine origin:

```bash
python3 deploy/node-public/check.py --probe https://api.lakeward.net
python3 deploy/node-public/check.py --probe https://wsapi.lakeward.net
```

The probe does not send credentials, follow redirects, print response bodies,
or create registrations. It expects 400 for empty bootstrap requests, 401 for
protected routes without credentials, and 404 for unreviewed machine paths.
A login redirect, generic edge 403, 429, or connection error fails the probe;
inspect the edge policy and rate limits before rerunning. Passing this probe
proves reachability and rejection behavior only, not authorized functionality.

Before closing KEV-35, record the actual edge configuration and verify a fresh
customer paxd without Cloudflare service-token headers: registration start,
browser approval, poll/one-time key retrieval, agent registration, control
WebSocket, ACP WebSocket, HTTP mailbox fallback, and artifact publication.
Check browser/user/admin rejection separately and verify origin bypass fails.
Do not call the launch ready until the remaining P0 gates also pass.

## Tunnel references

- [Cloudflare Tunnel architecture](https://developers.cloudflare.com/tunnel/):
  cloudflared establishes outbound connections; public inbound ports are not
  required for Tunnel.
- [Local ingress configuration](https://developers.cloudflare.com/tunnel/advanced/local-management/configuration-file/):
  hostname/path routing and ingress validation for locally managed tunnels.
- [Access path precedence](https://developers.cloudflare.com/cloudflare-one/access-controls/policies/app-paths/):
  path overrides and wildcard matching; runtime method/template enforcement
  remains necessary.

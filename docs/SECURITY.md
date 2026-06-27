# Security Plan

pax-manager is intended to run on Google Cloud Run behind Cloudflare Access.

## Transport

Cloud Run terminates TLS for the public service. The application does not manage
certificates directly.

## Dashboard Authentication

Cloudflare Access is the authentication layer for browser users.

- Cloudflare validates the identity provider login.
- Cloudflare forwards authenticated requests to Cloud Run.
- pax-manager validates `Cf-Access-Jwt-Assertion` against the configured
  Cloudflare Access issuer, audience, expiry, and JWKS signature.
- Local development can opt out of Cloudflare JWT validation with
  `CLOUDFLARE_ACCESS_DISABLED=true` and then opt into `X-User-Email` and
  `LOCAL_USER_ID` fallback with `ALLOW_LOCAL_USER_HEADER=true`.

Production deployments should keep Cloudflare Access validation enabled and
must not enable local header fallback.

## Agent Authentication

There are two agent credential types.

### Agent Registration Tokens

Agent registration requires an owner-bound one-time registration token.

Preferred flow:

1. A browser user calls `POST /api/user/agent-registration-tokens`.
2. pax-manager stores only the token hash in `agent_registration_tokens`.
3. paxd sends the plain token once in `X-Registration-Token`.
4. pax-manager marks the token as used and creates the agent under that owner.

`REGISTRATION_TOKEN` remains as a bootstrap escape hatch. When used, ownership
comes from `REGISTRATION_TOKEN_OWNER_EMAIL`, or `LOCAL_USER_ID` if unset.

### Agent API Keys

paxd receives an agent API key during registration and uses it for cloud
websocket connections and authenticated agent HTTP calls.

- Plain keys are returned once.
- pax-manager stores only SHA-256 hashes.
- Keys are tied to `agent_id`.

paxd can authenticate to:

```text
/api/agent/ws?agent_id=<agentId>&session_id=<sessionId>
```

with:

```http
X-Pax-Key: <agentApiKey>
```

The websocket handshake validates `X-Pax-Key` before upgrade. If `agent_id` is
present, it must match the authenticated key.

After registration, pax-manager returns:

- `agentId`
- `apiKey`

The plain API key is returned once and stored by paxd. pax-manager stores only
the SHA-256 hash. Agent HTTP endpoints after registration require:

```http
Authorization: Bearer <apiKey>
```

## Authorization

pax-manager is multi-tenant at the application and schema level.

- `users` stores Cloudflare/local identities.
- `agents.owner_user_id` is the tenant boundary for each agent.
- `mailbox.owner_user_id` stores the agent owner for direct filtering.
- `agent_sessions` inherits ownership through `agent_id`.
- Normal user APIs filter by the caller's `user_id`.
- Built-in admin users and users listed in the current `ADMIN_EMAILS` setting
  bypass tenant filters. Persisted `users.role` is not used as the source of
  admin authority.

Built-in admin emails:

- `toddzheng024@gmail.com`
- `gengcongkai456789@gmail.com`
- `zhangjiahang0725@gmail.com`

Cross-tenant access should return `404` for object-specific routes so callers
cannot distinguish missing records from records owned by another user.

## Data Handling

- Hermes local API keys stay on the agent machine and are never uploaded.
- Mailbox payloads and results are stored in PostgreSQL.
- Completed mailbox rows should be deleted or archived by a scheduled cleanup
  job once retention policy is finalized.

## Operational Notes

- Keep Cloud Run ingress restricted when relying on Cloudflare headers.
- Rotate registration tokens after bootstrapping a fleet.
- Prefer short mailbox TTLs for steer messages because they are time-sensitive.
- API routes enforce a configurable request body cap with `MAX_BODY_BYTES`.
- API routes use per-client in-memory rate limits. Defaults are 300 requests per
  minute with burst 60 for `/api/*`, and 30 requests per minute with burst 10
  for `/api/agent/register`.
- Cloud Build deploys set Cloud Run `max-instances` to 2 as an operational
  compromise because ACP tunnel WebSockets are tracked in process memory. Keep
  pax-manager scale low until ACP tunnels have a cross-instance backplane. Use
  Cloud Armor in front of Cloud Run for network-edge rate limits and path rules.

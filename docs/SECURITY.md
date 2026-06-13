# Security Plan

pax-manager is intended to run on Google Cloud Run behind Cloudflare Access.

## Transport

Cloud Run terminates TLS for the public service. The application does not manage
certificates directly.

## Dashboard Authentication

Cloudflare Access is the authentication layer for browser users.

- Cloudflare validates the identity provider login.
- Cloudflare forwards authenticated requests to Cloud Run.
- pax-manager reads `Cf-Access-Authenticated-User-Email`.
- Local development can opt into `X-User-Email` and `LOCAL_USER_ID` fallback
  with `ALLOW_LOCAL_USER_HEADER=true`.

The application must run behind Cloudflare Access or another trusted ingress
that strips spoofed Cloudflare identity headers. Do not expose it directly to
the public internet while trusting identity headers.

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

### User Platform API Keys

Users can create long-lived API keys for paxd cloud websocket connections.

- Plain keys are returned once.
- pax-manager stores only SHA-256 hashes.
- Keys are tied to `owner_user_id`.
- Revoked keys are rejected.
- Successful websocket authentication updates `last_used_at`.

paxd can authenticate to:

```text
/api/agent/ws?key=<apiKey>
```

or with:

```http
Authorization: Bearer <apiKey>
```

After registration, pax-manager returns:

- `agentId`
- `apiKey`

The plain API key is returned once and stored by paxd. pax-manager stores only
the SHA-256 hash. All agent endpoints after registration require:

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

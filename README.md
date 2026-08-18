# pax-manager

[![CI](https://github.com/pax-beehive/pax-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/pax-beehive/pax-manager/actions/workflows/ci.yml)

pax-manager is the Fleet Control Plane API for paxd agents. It stores agent state,
session snapshots, and user-to-agent mailbox messages in PostgreSQL.

It also supports a staged opaque E2EE transport: encrypted browser commands and
paxd events are persisted in PostgreSQL, LISTEN/NOTIFY wakes the Manager instance
holding the relevant WebSocket or SSE connection, and only Browser/paxd possess
the payload key. See `docs/handoff_20260806_143500.md` for the transport boundary
and rollout status.

The primary production path is websocket-based after registration:

```text
Dashboard -> pax-manager -> PostgreSQL mailbox
paxd      -> pax-manager websocket -> PostgreSQL status, mailbox, and offsets
paxd      -> Hermes API on localhost:8642
```

Agents only need outbound HTTPS. The cloud service never opens an inbound
connection to a machine running paxd.

## Current Scope

- Multi-tenant users, owner-scoped agents, and admin bypass.
- Agent registration with owner-bound one-time registration tokens.
- User-generated platform API key records.
- Agent status reports with paxd-compatible session fields.
- PostgreSQL-backed `users`, `agents`, `agent_sessions`, `mailbox`,
  `message_offsets`, `agent_registration_tokens`, and `user_api_keys` tables.
- User APIs for listing agents, listing sessions, sending mailbox messages, and
  checking mailbox state.
- An authenticated `/api/agent/ws` endpoint for paxd cloud communication.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `9879` | HTTP listen port. |
| `DATABASE_URL` | empty | PostgreSQL connection string. Empty uses in-memory storage for local development. |
| `REGISTRATION_TOKEN` | empty | Optional bootstrap token for agent registration. Prefer user-minted one-time tokens. |
| `REGISTRATION_TOKEN_OWNER_EMAIL` | empty | Owner email for the bootstrap registration token. Defaults to `LOCAL_USER_ID`. |
| `LOCAL_USER_ID` | `local@example.local` | Local development email used only when `ALLOW_LOCAL_USER_HEADER=true`. |
| `ALLOW_LOCAL_USER_HEADER` | `false` | Enables `X-User-Email` and `LOCAL_USER_ID` fallback for local development only. |
| `CLOUDFLARE_ACCESS_DISABLED` | `false` | Disables Cloudflare Access JWT validation for local development only. |
| `CLOUDFLARE_ACCESS_ISSUER` | empty | Expected Cloudflare Access JWT issuer, for example `https://<team>.cloudflareaccess.com`. |
| `CLOUDFLARE_ACCESS_AUD` | empty | Expected Cloudflare Access application audience. |
| `CLOUDFLARE_ACCESS_JWKS_URL` | empty | Cloudflare Access JWKS URL, usually `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`. |
| `ADMIN_EMAILS` | empty | Comma-separated extra admin email list. These are added to the built-in admin emails. |
| `MAX_BODY_BYTES` | `1048576` | Maximum request body size accepted by API handlers. |
| `API_RATE_LIMIT_PER_MINUTE` | `300` | Per-client request rate for `/api/*` routes. |
| `API_RATE_LIMIT_BURST` | `60` | Per-client burst size for `/api/*` routes. |
| `REGISTER_RATE_LIMIT_PER_MINUTE` | `30` | Per-client request rate for `/api/agent/register`. |
| `REGISTER_RATE_LIMIT_BURST` | `10` | Per-client burst size for `/api/agent/register`. |
| `OBJECT_STORAGE_BUCKET` | none | Required S3-compatible bucket for releases, session artifacts, publications, and attachments. |
| `OBJECT_STORAGE_REGION` | `us-east-1` | AWS signing region. Non-AWS services may use a configured logical region. |
| `OBJECT_STORAGE_ENDPOINT` | empty | Optional internal S3 API endpoint used by server-side `HeadObject` calls. Leave empty for AWS S3. |
| `OBJECT_STORAGE_PUBLIC_ENDPOINT` | `OBJECT_STORAGE_ENDPOINT` | Optional browser- and paxd-reachable endpoint used when creating presigned PUT/GET URLs. Leave empty for AWS S3. |
| `OBJECT_STORAGE_FORCE_PATH_STYLE` | `false` | Use path-style bucket URLs. Usually required by MinIO and many S3-compatible services. |

Built-in admin emails:

- `toddzheng024@gmail.com`
- `gengcongkai456789@gmail.com`
- `zhangjiahang0725@gmail.com`

Object storage credentials use the standard AWS SDK credential chain. Common choices are
`AWS_ACCESS_KEY_ID` plus `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, an ECS task role, or an
EC2 instance role. pax-manager does not load Google Cloud service-account or ID-token
credentials for artifact storage.

### AWS S3

For AWS S3, leave both endpoint variables empty:

```bash
export OBJECT_STORAGE_BUCKET=pax-production-artifacts
export OBJECT_STORAGE_REGION=us-west-2
export AWS_PROFILE=pax-production
```

The IAM identity needs `s3:GetObject` (used for both GET and HEAD requests),
`s3:GetObjectVersion` (used for version-pinned downloads), and `s3:PutObject` for
the configured bucket. Browser and paxd uploads use a direct `PUT` ticket with protocol
`s3_presigned_put`. They must send every returned header, including `Content-Type`,
`If-None-Match: *`, and, when present, `x-amz-meta-sha256` and
`x-amz-checksum-sha256`. The latter is the base64 form required by S3, while the API
continues to accept and store the hex digest. A supporting object store verifies this
checksum while receiving the body. The precondition makes a ticket write-once: it
cannot overwrite an object that already exists at the same key.

Enable bucket versioning for generation-pinned downloads. After the manager verifies
the stored synthetic generation with `HeadObject`, it includes the returned S3
`VersionId` in the presigned GET, preventing an overwrite between verification and
download from changing the bytes behind an issued URL. The bundled MinIO Compose
stacks enable versioning automatically. For AWS S3, enable it once with an account
allowed to manage the bucket, for example:

```bash
aws s3api put-bucket-versioning \
  --bucket "$OBJECT_STORAGE_BUCKET" \
  --versioning-configuration Status=Enabled
```

Unversioned S3-compatible buckets remain supported, but can only reject a stale
generation at signing time; they cannot bind the subsequent GET to that exact object
version if another credential overwrites the key concurrently.

If the PUT returns HTTP `412 Precondition Failed`, the object may be from an earlier
successful upload whose response was lost. Clients must continue to the matching
completion endpoint; pax-manager uses HEAD plus size, content type, and SHA-256
metadata to decide whether it is the intended object. Do not treat other 4xx responses
as success. This rule applies to node publications, attachments, and generic artifact
upload clients.

### MinIO or another S3-compatible service

Use separate internal and public endpoints when pax-manager reaches storage on a private
Docker or LAN address but clients use a public hostname:

```bash
export OBJECT_STORAGE_BUCKET=pax-artifacts
export OBJECT_STORAGE_REGION=us-east-1
export OBJECT_STORAGE_ENDPOINT=http://minio:9000
export OBJECT_STORAGE_PUBLIC_ENDPOINT=https://objects.example.com
export OBJECT_STORAGE_FORCE_PATH_STYLE=true
export AWS_ACCESS_KEY_ID=pax-manager
export AWS_SECRET_ACCESS_KEY='replace-with-a-secret'
```

The public hostname is part of the SigV4 signature and cannot be replaced after a URL is
created. A reverse proxy in front of the object store must preserve the original `Host`
header. Alternatively, use the same public endpoint for internal and public access and
ensure pax-manager can resolve and reach that hostname. Configure object-store CORS to allow
the dashboard origin to issue `PUT`, `GET`, and `HEAD` requests with `Content-Type`,
`If-None-Match`, `x-amz-meta-sha256`, and `x-amz-checksum-sha256` headers.

The main Compose stack passes `PAX_CONSOLE_ORIGIN` (development default
`http://localhost:3000`) to MinIO as `MINIO_API_CORS_ALLOW_ORIGIN`. Set it to the exact
console origin in production. Open-source MinIO exposes CORS as a server-wide setting,
not the paid per-bucket CORS API, so use a dedicated instance or account for this stack
when different buckets need different browser origins. The pinned image is smoke-tested
with a browser preflight for the signed `PUT` headers above.

The unattended installer and updater use `/api/v1/public/*`; do not place these routes
behind an interactive Cloudflare Access login. Configure an Access bypass or a
non-interactive service-auth policy. The same restriction applies to
`OBJECT_STORAGE_PUBLIC_ENDPOINT`: a `302` redirect to an Access login invalidates the
presigned request and prevents paxd or the browser from uploading and downloading.

Release publication endpoints require an admin user's existing platform API key as
`Authorization: Bearer <key>`. The object must already exist in
`OBJECT_STORAGE_BUCKET`; pax-manager verifies its size, content type, and
`x-amz-meta-sha256` with `HeadObject` before recording it. A request may send
`generation: 0`, in which case pax-manager stores a stable positive object fingerprint.

## Run

```bash
go run ./cmd/manager
```

With PostgreSQL:

```bash
DATABASE_URL='postgres://user:pass@localhost:5432/pax?sslmode=disable' go run ./cmd/manager
```

The server executes `db/init.sql` at startup.

## Project Layout

- `cmd/manager` contains only the production service entrypoint.
- `internal/manager` contains service configuration, Hertz handlers, auth,
  rate limiting, stores, models, and tests.
- `cmd/openapi-gen` contains the Thrift-to-OpenAPI and Hertz route generator.
- `api/pax_manager.thrift` is the API contract source of truth.

## API Contract

The OpenAPI document and IDL-backed Hertz route registration are generated from
the Thrift IDL at:

```text
api/pax_manager.thrift
```

After changing the IDL, regenerate checked-in derived source:

```bash
make generate
```

The generated route registration calls thin Hertz handler functions. Middleware
injects the manager service into each Hertz request context; handlers only bind
transport data and delegate to the service.

## Local Docker

Start PostgreSQL, MinIO, and pax-manager:

```bash
export MINIO_ROOT_USER=pax-local-admin
export MINIO_ROOT_PASSWORD="$(openssl rand -hex 32)"
export OBJECT_STORAGE_BUCKET=pax-artifacts
export OBJECT_STORAGE_PUBLIC_ENDPOINT=http://localhost:9000
export PAX_CONSOLE_ORIGIN=http://localhost:3000
make up
```

The `localhost` public endpoint is only correct when the browser and paxd run on the
same host. For remote clients, set it to an HTTPS hostname they can reach, such as
`https://objects.example.com`. Running `docker compose` directly requires the MinIO
credentials and public endpoint above; `make up` supplies `minioadmin` development
defaults only when they were not exported. Do not use those defaults on a public server.

`make up` creates the local `paxdb` database and the configured MinIO bucket
before starting pax-manager.

The bundled, version-pinned MinIO service is a single-node self-host/development option,
not a durability boundary. Back up `pax-manager-minio-data` to a different machine and
test restores, or use AWS S3 or another verified replicated service for production.
AWS S3 and the pinned MinIO version are supported by this repository. Before using any
other S3-compatible implementation, verify that it enforces conditional PutObject with
`If-None-Match: *` and validates `x-amz-checksum-sha256`; basic SigV4, PUT, and HEAD
compatibility alone is not sufficient.
For production, provision pax-manager with a separate MinIO service account restricted
to GetObject and PutObject on `OBJECT_STORAGE_BUCKET`, then export that account as
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` before starting Compose. Compose accepts
those explicit overrides plus an optional `AWS_SESSION_TOKEN`; it falls back to the
MinIO root credentials only for the bundled home/development setup.

Start PostgreSQL and MinIO for local `go run` development:

```bash
make db-up
make run
```

Reset only the local Postgres database and rerun `db/init.sql` on the next manager
start. This target preserves the MinIO artifact volume:

```bash
make db-reset
```

Start an isolated manager + PostgreSQL + MinIO + paxd integration stack:

```bash
make paxd-integration-up
make paxd-integration-logs
make paxd-integration-down
```

## Cloud Build

`cloudbuild.vm.yaml` is the active deployment path. It passes the configured
bucket, internal/public endpoints, path-style flag, and AWS access-key secret
names to `deploy/vm/deploy.sh`. The VM reads those credentials from Secret
Manager before starting pax-manager. Configure the substitutions in that file,
then run `make cloud-build-vm`.

`cloudbuild.yaml` is an archived Cloud Run build/push pipeline. Its deploy step
is intentionally disabled because the old Cloud Run rollback path does not
provision the now-required object-storage configuration and credentials. `make
cloud-build` does not deploy a service. Reintroducing Cloud Run requires explicit
`OBJECT_STORAGE_*` environment variables and an AWS credential source first.

## Database

The DDL is checked in at:

```text
db/init.sql
```

It can be applied manually with:

```bash
psql "$DATABASE_URL" -f db/init.sql
```

## Agent API

The OpenAPI document is served by the running service:

```text
GET /openapi
GET /openapi.json
```

`/openapi` serves a lightweight HTML viewer. `/openapi.json` serves the raw
OpenAPI document. The JSON document uses the request host and forwarded protocol
as its OpenAPI `servers[0].url`, so the same endpoints work behind Cloud Run
and Cloudflare.

All agent endpoints except registration require:

```http
Authorization: Bearer <apiKey>
```

### Register

```http
POST /api/agent/register
Content-Type: application/json
X-Registration-Token: <registrationToken>
```

```json
{
  "name": "workstation",
  "hostname": "workstation.local",
  "agentType": "hermes",
  "os": "linux"
}
```

Response:

```json
{
  "agentId": "agent_...",
  "apiKey": "pax_..."
}
```

### Report Status

```http
POST /api/agent/status
```

The session shape follows `../paxd/pkg/model.SessionInfo` field names:

```json
{
  "sessions": [
    {
      "sessionId": "sess-1",
      "agentType": "hermes",
      "nativeId": "response-1",
      "name": "repo task",
      "projectId": "repo",
      "preview": "fix failing tests",
      "workspaceRoots": ["/workspace/repo"],
      "status": "running",
      "currentTask": "go test ./...",
      "messageCount": 7,
      "tokenUsage": 123,
      "model": "gpt-test",
      "runId": "run-1",
      "runStatus": "running"
    }
  ]
}
```

### Pull Mailbox

```http
GET /api/agent/mailbox?offset=0&limit=10
GET /api/agent/sessions/{sessionId}/mailbox?offset=0&limit=10
```

Use the agent mailbox when one paxd process dispatches work for many sessions.
Use the session mailbox when the paxd connection is bound to one session.
Messages include a `payload` field containing a paxd-style envelope request. For
a normal chat message, the generated payload is:

```json
{
  "entity_type": "turn",
  "event_type": "start",
  "sessionId": "sess-1",
  "prompt": "run the tests"
}
```

### Report Result and Offset

```http
POST /api/agent/messages/{messageId}/result
POST /api/agent/messages/offset
```

## User API

User endpoints use Cloudflare Access JWT assertions. The trusted header is
`Cf-Access-Jwt-Assertion`, and the service validates the JWT issuer, audience,
expiry, and signature against Cloudflare Access JWKS.

For local-only development, set `CLOUDFLARE_ACCESS_DISABLED=true` and
`ALLOW_LOCAL_USER_HEADER=true` to allow `X-User-Email` or `LOCAL_USER_ID`
fallback. Do not enable that mode on a public deployment.

Users are stored in the `users` table. Normal users only see agents and mailbox
records where `ownerUserId` is their `userId`. Admin status is derived on each
request from the built-in admin list plus current `ADMIN_EMAILS`.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/user/agents` | List agents. |
| `GET` | `/api/user/agents/{agentId}` | Get one agent. |
| `GET` | `/api/user/agents/{agentId}/sessions` | List sessions for an agent. |
| `GET` | `/api/user/agents/{agentId}/sessions/{sessionId}` | Get one session under an agent. |
| `GET` | `/api/user/agents/{agentId}/messages` | List mailbox messages for an agent. |
| `POST` | `/api/user/agents/{agentId}/messages` | Create a bootstrap or agent-level mailbox message. |
| `GET` | `/api/user/agents/{agentId}/sessions/{sessionId}/messages` | List mailbox records for a session under an agent. |
| `POST` | `/api/user/agents/{agentId}/sessions/{sessionId}/messages` | Create a mailbox message for a specific session. |
| `GET` | `/api/user/api-keys` | List user platform API keys. |
| `POST` | `/api/user/api-keys` | Create a user platform API key. |
| `DELETE` | `/api/user/api-keys/{keyId}` | Revoke a platform API key. |
| `POST` | `/api/user/agent-registration-tokens` | Mint an owner-bound one-time agent registration token. |

The agent-scoped message create route is for bootstrap or agent-level commands.
Once a session exists, send messages through the session-scoped route.

### Logical Projects

Logical Projects group sessions separately from repositories. Reusable Targets
bind a Project to an Agent and working-directory intent:

| Method | Path | Description |
| --- | --- | --- |
| `POST` / `GET` | `/api/v1/user/{user_id}/projects` | Create or list Projects. |
| `GET` / `PATCH` | `/api/v1/user/{user_id}/projects/{project_id}` | Read, rename, or move a Project. |
| `POST` | `/api/v1/user/{user_id}/projects/{project_id}/archive` | Soft-archive a Project. |
| `POST` / `GET` | `/api/v1/user/{user_id}/projects/{project_id}/targets` | Create or list reusable workspace Targets. |
| `GET` / `PATCH` | `/api/v1/user/{user_id}/projects/{project_id}/targets/{target_id}` | Read or update a Target. |

`agent_sessions.primary_project_id` is optional and immutable after creation.
The flat Session list accepts a `primary_project_id` filter. New project
Sessions use the existing node/agent Conversation endpoint with
`primary_project_id` and optional `project_target_id`; the Manager creates the
native ACP session before persisting the PAX Session. See
[`docs/logical_projects.md`](docs/logical_projects.md) for persistence,
ownership, and launch semantics.

### Create a Platform API Key

Users can generate platform API key records:

```http
POST /api/user/api-keys
Content-Type: application/json
```

```json
{
  "name": "automation"
}
```

The plain key is returned once:

```json
{
  "apiKey": {
    "keyId": "key_...",
    "ownerUserId": "usr_...",
    "name": "automation",
    "prefix": "paxu_..."
  },
  "key": "paxu_..."
}
```

The paxd websocket uses the agent API key returned by agent registration, not
the user platform API key. Pass the agent key as `X-Pax-Key` during the
websocket handshake:

```text
X-Pax-Key: pax_...
ws://localhost:9879/api/agent/ws?agent_id=agent_...&session_id=sess-...
```

The server stores only the key hash. The websocket handshake verifies that the
optional `agent_id` matches the authenticated paxd key.

After the server accepts the websocket, it sends:

```json
{
  "type": "connected",
  "code": 200,
  "message": "ok",
  "data": {
    "agent_id": "agent_...",
    "owner_user_id": "usr_..."
  }
}
```

paxd sends request frames:

```json
{
  "type": "pull_mailbox",
  "request_id": "pull-1",
  "data": {
    "offset": 0,
    "limit": 10
  }
}
```

Supported frame types:

| Type | Data |
| --- | --- |
| `status` or `report_status` | Agent status report. |
| `pull_mailbox` | `offset` and optional `limit`. |
| `update_offset` | `offset`. |
| `message_result` or `report_message_result` | `message_id`, `status`, `result`, and `error`. |

Responses use:

```json
{
  "type": "pull_mailbox_result",
  "request_id": "pull-1",
  "code": 200,
  "message": "ok",
  "data": {}
}
```

### Create an Agent Registration Token

```http
POST /api/user/agent-registration-tokens
Content-Type: application/json
```

```json
{
  "expiresInSeconds": 3600
}
```

Admins can mint a token for another user:

```json
{
  "ownerEmail": "teammate@example.com",
  "expiresInSeconds": 3600
}
```

## Test

```bash
GOCACHE=/tmp/pax-manager-go-cache go test ./...
```

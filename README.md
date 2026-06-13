# pax-manager

pax-manager is the Fleet Control Plane API for paxd agents. It stores agent state,
session snapshots, and user-to-agent mailbox messages in PostgreSQL.

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

Built-in admin emails:

- `toddzheng024@gmail.com`
- `gengcongkai456789@gmail.com`
- `zhangjiahang0725@gmail.com`

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

Start Postgres and pax-manager:

```bash
make up
```

Start only Postgres for local `go run` development:

```bash
make db-up
make run
```

Reset the local Postgres volume and rerun `db/init.sql`:

```bash
make db-reset
```

## Cloud Build

`cloudbuild.yaml` builds the main Dockerfile, pushes the image to Artifact
Registry, and deploys the new image to an already configured Cloud Run service.
It does not manage the Cloud SQL mount or the `DATABASE_URL` secret. Configure
those on the Cloud Run service once, then Cloud Build only rolls forward the
container image.

Required setup:

```bash
gcloud services enable \
  artifactregistry.googleapis.com \
  cloudbuild.googleapis.com \
  run.googleapis.com \
  secretmanager.googleapis.com \
  sqladmin.googleapis.com

gcloud artifacts repositories create pax-manager \
  --repository-format=docker \
  --location=us-west1

gcloud sql databases create paxdb \
  --instance=pax-manager-postgres

gcloud sql users create pax \
  --instance=pax-manager-postgres \
  --password='REPLACE_WITH_STRONG_PASSWORD'
```

Create or update the `DATABASE_URL` secret using the Cloud SQL Unix socket path.
The keyword/value DSN avoids URL-encoding issues in passwords.

```bash
printf '%s' 'user=pax password=REPLACE_WITH_PASSWORD dbname=paxdb host=/cloudsql/PROJECT_ID:us-west1:pax-manager-postgres sslmode=disable' \
  > /tmp/pax-manager-database-url.txt

gcloud secrets create pax-manager-database-url \
  --data-file=/tmp/pax-manager-database-url.txt
```

If the secret already exists, add a new version instead:

```bash
gcloud secrets versions add pax-manager-database-url \
  --data-file=/tmp/pax-manager-database-url.txt
```

Grant the Cloud Run runtime service account Cloud SQL access. If you use the
default Compute Engine service account, it is usually:
`PROJECT_NUMBER-compute@developer.gserviceaccount.com`.

```bash
gcloud projects add-iam-policy-binding PROJECT_ID \
  --member='serviceAccount:PROJECT_NUMBER-compute@developer.gserviceaccount.com' \
  --role='roles/cloudsql.client'
```

Configure the Cloud Run service once with the Cloud SQL mount and database
secret:

```bash
gcloud run services update pax-manager \
  --region=us-west1 \
  --add-cloudsql-instances=PROJECT_ID:us-west1:pax-manager-postgres \
  --update-secrets=DATABASE_URL=pax-manager-database-url:latest
```

Submit a build:

```bash
gcloud builds submit \
  --substitutions=_REGION=us-west1,_REPOSITORY=pax-manager,_SERVICE=pax-manager,_MAX_INSTANCES=5,_TIMEOUT=30s,_CLOUDFLARE_ACCESS_ISSUER=https://billowing-dream-9314.cloudflareaccess.com,_CLOUDFLARE_ACCESS_AUD=1a59397a05310415570607d5dbfa973e1bcfb74a7a4617c9bc859112cfa0efca,_CLOUDFLARE_ACCESS_JWKS_URL=https://billowing-dream-9314.cloudflareaccess.com/cdn-cgi/access/certs
```

The deployed service validates `Cf-Access-Jwt-Assertion` from Cloudflare Access
by default. Do not set `CLOUDFLARE_ACCESS_DISABLED=true` or
`ALLOW_LOCAL_USER_HEADER=true` in Cloud Run.

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
```

Messages include a `payload` field containing a paxd-style envelope request.
For a normal chat message, the generated payload is:

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
| `GET` | `/api/user/agents/{agentId}/sessions` | List sessions for an agent. |
| `GET` | `/api/user/sessions/{sessionId}` | Get a session snapshot. |
| `GET` | `/api/user/sessions/{sessionId}/messages` | List mailbox records for a session. |
| `POST` | `/api/user/message` | Create a mailbox message. |
| `GET` | `/api/user/mailbox` | List mailbox messages. |
| `GET` | `/api/user/api-keys` | List user platform API keys. |
| `POST` | `/api/user/api-keys` | Create a user platform API key. |
| `DELETE` | `/api/user/api-keys/{keyId}` | Revoke a platform API key. |
| `POST` | `/api/user/agent-registration-tokens` | Mint an owner-bound one-time agent registration token. |

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

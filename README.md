# pax-manager

pax-manager is the Fleet Control Plane API for paxd agents. It stores agent state,
session snapshots, and user-to-agent mailbox messages in PostgreSQL.

The primary production path is pull-based:

```text
Dashboard -> pax-manager -> PostgreSQL mailbox
paxd      -> pax-manager -> PostgreSQL status and mailbox offset
paxd      -> Hermes API on localhost:8642
```

Agents only need outbound HTTPS. The cloud service never opens an inbound
connection to a machine running paxd.

## Current Scope

- Multi-tenant users, owner-scoped agents, and admin bypass.
- Agent registration with owner-bound one-time registration tokens.
- User-generated platform API keys for paxd cloud websocket connections.
- Agent status reports with paxd-compatible session fields.
- PostgreSQL-backed `users`, `agents`, `agent_sessions`, `mailbox`,
  `message_offsets`, `agent_registration_tokens`, and `user_api_keys` tables.
- User APIs for listing agents, listing sessions, sending mailbox messages, and
  checking mailbox state.
- A narrow legacy `/api/agent/ws` endpoint for existing websocket experiments.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `9879` | HTTP listen port. |
| `DATABASE_URL` | empty | PostgreSQL connection string. Empty uses in-memory storage for local development. |
| `REGISTRATION_TOKEN` | empty | Optional bootstrap token for agent registration. Prefer user-minted one-time tokens. |
| `REGISTRATION_TOKEN_OWNER_EMAIL` | empty | Owner email for the bootstrap registration token. Defaults to `LOCAL_USER_ID`. |
| `LOCAL_USER_ID` | `local@example.local` | Local development email used only when `ALLOW_LOCAL_USER_HEADER=true`. |
| `ALLOW_LOCAL_USER_HEADER` | `false` | Enables `X-User-Email` and `LOCAL_USER_ID` fallback for local development only. |
| `ADMIN_EMAILS` | empty | Comma-separated extra admin email list. These are added to the built-in admin emails. |

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
Registry, and deploys to Cloud Run.

Required setup:

```bash
gcloud artifacts repositories create pax-manager \
  --repository-format=docker \
  --location=us-west1

gcloud secrets create pax-manager-database-url \
  --data-file=/path/to/database-url.txt
```

Submit a build:

```bash
gcloud builds submit \
  --substitutions=_REGION=us-west1,_REPOSITORY=pax-manager,_SERVICE=pax-manager
```

The deployed service expects Cloudflare Access or another trusted ingress to
provide `Cf-Access-Authenticated-User-Email`. Do not enable
`ALLOW_LOCAL_USER_HEADER` in Cloud Run.

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

User endpoints use Cloudflare Access identity headers. The trusted header is
`Cf-Access-Authenticated-User-Email`.

For local-only development, set `ALLOW_LOCAL_USER_HEADER=true` to allow
`X-User-Email` or `LOCAL_USER_ID` fallback. Do not enable that mode on a public
deployment.

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
| `POST` | `/api/user/api-keys` | Create a platform API key for paxd cloud websocket use. |
| `DELETE` | `/api/user/api-keys/{keyId}` | Revoke a platform API key. |
| `POST` | `/api/user/agent-registration-tokens` | Mint an owner-bound one-time agent registration token. |

### Create a Platform API Key

Users can generate API keys for paxd cloud websocket connections:

```http
POST /api/user/api-keys
Content-Type: application/json
```

```json
{
  "name": "workstation paxd"
}
```

The plain key is returned once:

```json
{
  "apiKey": {
    "keyId": "key_...",
    "ownerUserId": "usr_...",
    "name": "workstation paxd",
    "prefix": "paxu_..."
  },
  "key": "paxu_..."
}
```

Use it for the cloud websocket:

```text
ws://localhost:9879/api/agent/ws?key=paxu_...
```

The server stores only the key hash. Revoked keys are rejected.

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

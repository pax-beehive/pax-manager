# paxd Integration Test Plan

## Purpose

This document defines what inputs are needed to run a containerized paxd
integration test against pax-manager, where each input comes from, and which
test phases produce values for later phases.

The target integration shape is:

```text
pax-manager container + PostgreSQL
        ^
        |
        v
paxd integration container
        |
        v
Hermes-compatible runtime in the same paxd container
```

The `paxd/Dockerfile.integration` image starts paxd and a Hermes-compatible mock
server in the same container. The mock server does not call an LLM provider and
does not need an LLM API key.

## Parameter Sources

| Parameter | Required | Source | Notes |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | Test environment | Passed to pax-manager. In compose this is fixed to the PostgreSQL service URL. |
| `PORT` | yes | Test environment | Passed to pax-manager. Default is `9879`. |
| `CLOUDFLARE_ACCESS_DISABLED` | yes | Test environment | Must be `true` for local container tests unless Cloudflare Access JWTs are provided. |
| `ALLOW_LOCAL_USER_HEADER` | yes | Test environment | Must be `true` so the test driver can use `X-User-Email`. |
| `X-User-Email` | yes | Test driver | Used to create the local test user and call user APIs. |
| Node registration token | yes | Produced by user API setup step | Created by `POST /api/v1/user/self/node-registration-tokens`. |
| `PAX_CLOUD_URL` | yes | Compose/test environment | URL paxd uses to reach pax-manager. In the driver network this is `http://host.docker.internal:<port>`. |
| `PAX_NODE_ID` | yes | Produced by node registration step | The driver pre-registers the node before starting paxd. |
| `PAX_NODE_API_KEY` | yes | Produced by node registration step | Lets paxd authenticate to `/api/v1/node/*` without self-registering. |
| `PAX_AGENT_ID` | yes | Produced by create-agent step | Created by `POST /api/v1/user/self/nodes/:node_id/agents`. paxd uses it to poll `/api/v1/node/agents/:agent_id/mailbox`. |
| `PAX_NODE_NAME` | no | Test environment | Human-readable node name. |
| `PAX_MACHINE_TYPE` | no | Test environment | Metadata reported during node registration. |
| `PAXD_DB_PATH` | yes | Test environment | SQLite path inside paxd container. Default in integration image is `/data/paxd.db`. |
| `HERMES_API_ENDPOINT` | yes | Test environment | For mock Hermes, use `http://127.0.0.1:8642` inside the paxd container. |
| `PAXD_START_MOCK_HERMES` | optional | Test environment | Default `true`. Keep the default for this integration path. |

## Phase Flow

### Phase 1: Start pax-manager dependencies

Inputs provided by the test runner:

- PostgreSQL container environment.
- pax-manager container environment:
  - `DATABASE_URL`
  - `PORT`
  - `CLOUDFLARE_ACCESS_DISABLED=true`
  - `ALLOW_LOCAL_USER_HEADER=true`
  - `LOCAL_USER_ID`
  - `ADMIN_EMAILS`

Outputs:

- A reachable pax-manager base URL.

### Phase 2: Create user-scoped setup data

Inputs provided by the test runner:

- pax-manager base URL.
- `X-User-Email`, for example `local@example.local`.

API calls:

```http
POST /api/v1/user/self/node-registration-tokens
X-User-Email: local@example.local
Content-Type: application/json

{}
```

Outputs:

- Node registration token.

### Phase 3: test driver pre-registers the node

The driver pre-registers the node before starting paxd because the cloud agent
must be created under a known node ID.

Inputs provided by previous phase:

- Node registration token.

API calls:

```http
POST /api/v1/node/register
X-Registration-Token: <registrationToken>
Content-Type: application/json

{
  "name": "integration-node",
  "hostname": "integration-node",
  "machine_type": "docker",
  "os": "linux",
  "arch": "amd64"
}
```

Outputs:

- `PAX_NODE_ID`
- `PAX_NODE_API_KEY`

### Phase 4: Create or select cloud agents under the node

Inputs:

- pax-manager base URL.
- `X-User-Email`.
- `node_id`.

API call:

```http
POST /api/v1/user/self/nodes/:node_id/agents
X-User-Email: local@example.local
Content-Type: application/json

{
  "name": "hermes-a",
  "agent_type": "hermes"
}
```

Outputs:

- `agent_id`, passed to paxd as `PAX_AGENT_ID` for the single-agent shortcut.

For multiple agents, provide a paxd config file with `agents[]` instead of only
using `PAX_AGENT_ID`.

Example:

```yaml
agents:
  - agent_id: agent_1
    instance_id: hermes-a
    name: hermes-a
    agent_type: hermes
    api_endpoint: http://127.0.0.1:8642
    enabled: true
  - agent_id: agent_2
    instance_id: hermes-b
    name: hermes-b
    agent_type: hermes
    api_endpoint: http://127.0.0.1:8643
    enabled: true
```

### Phase 5: Start paxd container

Inputs:

- No LLM provider API key is required.
- `PAXD_START_MOCK_HERMES` can be omitted because the default is `true`.
- `HERMES_API_ENDPOINT=http://127.0.0.1:8642`.
- `PAX_NODE_ID`, `PAX_NODE_API_KEY`, and `PAX_AGENT_ID` come from earlier setup
  phases.

Outputs:

- paxd registers or loads node identity.
- paxd syncs configured cloud agents into local `cloud_agents`.
- paxd starts polling mailbox messages.
- paxd periodically reports node status with `agents[]`.

### Phase 6: Exercise user-to-agent message flow

Inputs:

- `node_id`
- `agent_id`
- session ID selected by the test, for example `sess-integration-1`

API calls:

```http
POST /api/v1/user/self/nodes/:node_id/agents/:agent_id/sessions
X-User-Email: local@example.local
Content-Type: application/json

{
  "session_id": "sess-integration-1",
  "name": "integration session"
}
```

```http
POST /api/v1/user/self/nodes/:node_id/agents/:agent_id/sessions/sess-integration-1/messages
X-User-Email: local@example.local
Content-Type: application/json

{
  "message": "run the integration test",
  "message_type": "chat"
}
```

Expected paxd behavior:

- Pulls the pending mailbox message.
- Marks it delivered.
- Sends it to Hermes or mock Hermes.
- Reports message result.
- Creates a node outbound message.

## BDD Scenarios

### Scenario: paxd uses a pre-registered node identity

Given the test driver has registered a node through `/api/v1/node/register`
And the test driver has created a cloud agent under that node
When paxd starts with `PAX_NODE_ID`, `PAX_NODE_API_KEY`, `PAX_CLOUD_URL`, and `PAX_AGENT_ID`
Then paxd does not need `PAX_REGISTRATION_TOKEN`
And paxd loads the preseeded node identity into local `node_state`
And paxd polls mailbox messages using `X-Pax-Key`

### Scenario: user message is delivered to mock Hermes

Given paxd is running with mock Hermes enabled
And the user has created a session under a node agent
When the user posts a chat message to the session message endpoint
Then paxd pulls the message from `/api/v1/node/agents/:agent_id/mailbox`
And paxd marks the message delivered
And paxd sends the prompt to mock Hermes
And paxd reports the message result as completed
And paxd creates a node outbound message with the Hermes response content

### Scenario: one node hosts multiple agents

Given a node has two cloud agents
And paxd has an `agents[]` config entry for each cloud agent
When paxd reports status
Then the node status payload contains both agents in `agents[]`
And each agent can include its own sessions
When the user sends a message to the first agent
Then paxd routes the message to the first agent runtime
When the user sends a message to the second agent
Then paxd routes the message to the second agent runtime

## Open Implementation Work

- Add an automated test driver that performs phases 2 through 6 and exports the
  generated values into the paxd compose service.
- Decide whether the canonical integration path should use paxd self-registration
  or test-driver pre-registration.
- Add assertions that the final user-facing message list contains both the
  original `user_to_node` message and the `node_to_user` outbound response.
- Design a separate real-Hermes test image if model-backed CI coverage becomes
  necessary.

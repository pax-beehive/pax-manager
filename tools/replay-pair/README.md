# Paxd reconnect pair fixture

This is an isolated, standard-library-only integration helper. It does not change
the paxd or Manager modules. Its durable files belong to the test fixture.

Build the binary used in the Linux/arm64 test image:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOWORK=off go build -trimpath -o pair-fixture-linux-arm64 .
```

Copy that binary to `/app/pair-fixture` in the integration image.

## HTTP control

```sh
/app/pair-fixture control GET /v1/status
/app/pair-fixture control POST /v1/remotes /fixture/control.json
/app/pair-fixture control --socket /root/.paxd/paxd.sock POST /v1/agent-connections /fixture/control.json
/app/pair-fixture http GET http://127.0.0.1:9880/_fixture/status
/app/pair-fixture http POST http://127.0.0.1:9880/_fixture/drop
```

The fixed positional order is `METHOD PATH [BODYFILE]` for `control`, and
`METHOD URL [BODYFILE]` for `http`. Optional flags precede the method. A body
filename of `-` reads stdin. Both commands print the response body unchanged;
HTTP status codes at least 400 also return a nonzero exit status. Requests with
bodies use `Content-Type: application/json`. `--timeout` defaults to `60s` and
`--command-id` optionally sets `X-Pax-Command-Id`. `control` defaults to
`/root/.paxd/paxd.sock`.

## ACP worker

Use the worker command `['/app/pair-fixture', 'mock-acp']` with `/fixture` mounted
into the paxd container. It implements line-delimited JSON-RPC:

1. `initialize` returns protocol version 1, empty session capabilities, prompt
   capabilities, agent information, and no authentication methods.
2. `session/new` immediately emits `available_commands_update` with the command
   `inspect` and native session ID `fixture-native-1`. It writes
   `/fixture/new-started` containing its PID, then waits for the file
   `/fixture/release-new` before replying with that native session ID.
3. `session/prompt` emits one `agent_message_chunk` containing `fixture reply`,
   followed by a result with `stopReason: end_turn`.

`/fixture/events.jsonl` contains one `kind: request` record per input message,
with `method`, raw JSON-RPC `id`, `pid`, and UTC `time`. Completion records have
`kind: new_result` or `kind: prompt_result`. Count only `kind == request` when
checking that reconnect did not dispatch a request twice. Prompt content and
credentials are not recorded. `--fixture-dir`, `--native-id`, and `--text` can
override the defaults. Use a fresh fixture directory for each test: the worker
does not delete prior gates or events.

## Reconnect proxy

```sh
/app/pair-fixture proxy --upstream http://manager:9879
```

The default listener is `:9880`; override it with `--listen`. Normal HTTP and
WebSocket traffic is forwarded to the upstream. Run the proxy as a separate
container and set paxd's cloud URL to that container's address.

`GET /_fixture/status` returns:

```json
{"active_websockets":2,"total_ws_connections":2,"drops":0}
```

`POST /_fixture/drop` closes all currently hijacked downstream WebSocket
connections, returns `{"dropped":2}`, and increments `drops` once per invocation.
Ordinary HTTP connections and the listener remain available. The total counts
successful WebSocket upgrades across reconnects. The fixture endpoints are for
the isolated test network only.

## Verification

```sh
GOWORK=off go test ./... -count=1 -coverprofile=coverage.out
GOWORK=off go test -race ./... -count=1
GOWORK=off go vet ./...
```

## Paired daemon and Manager acceptance

Use a fresh Manager integration stack with Postgres and object storage. Build the
actual paxd daemon for the same Linux architecture as its runtime image, with
CGO enabled for SQLite. Build this helper for that architecture, then copy both
binaries into a test image as `/app/paxd` and `/app/pair-fixture`. A cached paxd
runtime image can be used with `docker create`, `docker cp`, and `docker commit`;
there is no need to publish it to a registry.

From the Manager repository, run:

```sh
GOWORK=off INTEGRATION_BASE_URL=http://127.0.0.1:39879 \
  PAXD_REPLAY_IMAGE=paxd-replay-pair:local \
  PAXD_REPLAY_NETWORK=your-isolated-docker-network \
  PAXD_REPLAY_MANAGER_URL=http://MANAGER_CONTAINER_IP:9879 \
  go test -tags=integration ./integration \
    -run '^TestActualPaxdReplayAcrossReconnectIntegration$' -count=1 -v
```

The Manager URL is the address reachable from inside the Docker network; the
base URL is reachable from the test runner. On Docker Desktop, use numeric
container IPs when local Docker DNS is unavailable. The test creates fresh
daemon and worker state, overrides the daemon DB path, and deletes its own
containers. It never mounts the installed daemon's state. Local API bodies are
sent over stdin to avoid stale reads when a host repeatedly overwrites the same
bind-mounted file.

Acceptance requires the early catalog to reach the canonical Manager session,
the pending creation result and a subsequent prompt result to reach the user
relay after forced reconnect, a durable native/Manager ID binding in the stopped
daemon's SQLite database, and exactly one initialize/new/prompt worker call.

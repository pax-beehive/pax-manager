# Search deployment

Search is an optional Elasticsearch projection. PG remains the source of truth.

Automatic Secretary generation is controlled independently by each user's
saved agent selection and enable switch, defaulting to off. It requires the
selected owned agent's normal ACP connection, and persists summaries even while
ES is unavailable. Deploy the Manager's complete `db/` directory and the Console
settings screen; no extra model key or local prototype daemon is needed. See
[secretary_automation.md](secretary_automation.md) for lifecycle and verification.

The Manager worker initializes the index and backfills source IDs in the
background, then claims durable coalesced work. An unavailable search backend
does not prevent chat persistence or Manager startup. Failed work stays retryable.

## Configuration

- `SEARCH_URL`: private HTTP(S) backend URL. Empty disables the search backend.
- `SEARCH_INDEX`: defaults to `pax-search-v1`. Use a new name for incompatible mappings.
- `SEARCH_API_KEY`: optional Elasticsearch API key, passed only in an authorization header.
- `SEARCH_READ_RECEIPT_SECRET`: a shared random secret of at least 32 bytes for
  source read receipts. Configure the same value across Manager instances and
  restarts. If omitted, each process generates an ephemeral key; callers must
  reread their sources after restart before publishing a summary.

The index has one primary shard and zero replicas for this single-node MVP.
The pinned container version is `9.5.5`, verified against the
[official Elastic image registry](https://www.docker.elastic.co/r/elasticsearch/elasticsearch)
and [single-node installation guide](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-elasticsearch-docker-basic).
Allow 2 GiB container memory; the configured heap is 512 MiB. Apply the host
prerequisites in the official guide, including `vm.max_map_count`, before startup.
Revisit memory and shard count using measured corpus size and query latency.

## Local development

For the complete reproducible acceptance flow, run `make search-acceptance`.
It creates disposable PG/ES services on random loopback ports, requires the core
checks to pass without skips, and cleans up its own resources. See
[search_acceptance.md](search_acceptance.md) for expected results.

The normal and integration Compose files include a private Elasticsearch service
with a persistent volume. They do not publish its ports. The Manager talks to
`http://elasticsearch:9200`. No extra model, embedding, or Chinese tokenizer is
required. Compose deliberately does not make Manager startup depend on search
health; its background worker retries initialization.

For isolated backend tests only:

```bash
docker compose -p pax-search-test -f docker-compose.search-test.yml up -d --wait
SEARCH_TEST_URL=http://127.0.0.1:19200 GOWORK=off GOCACHE=/tmp/pax-manager-go-cache go test -tags=integration -count=1 ./internal/manager/search
docker compose -p pax-search-test -f docker-compose.search-test.yml down -v
```

The test stack publishes only loopback ports. Its project and volumes are
separate from normal development data. Delete only this isolated stack's volumes.

## Existing VM

ES runs as a separate Docker container, independently of the Manager image.
`deploy/vm/search.sh start` creates the private `pax-search-private` network and
`pax-search-data` volume, starts `pax-search` with `unless-stopped`, waits for
cluster readiness, and attaches an existing `pax-manager` container. No host port
is published. Repeating `start` preserves the volume and repairs missing network
membership. `stop` preserves both container and data.

```bash
deploy/vm/search.sh start
deploy/vm/search.sh status
deploy/vm/search.sh health
```

The helper uses the pinned image, 2 GiB memory limit, 512 MiB JVM heap and a
Docker health check. `SEARCH_READY_ATTEMPTS` defaults to 60 and
`SEARCH_READY_INTERVAL` to two seconds; each probe is bounded to five seconds.
`MANAGER_CONTAINER`, `SEARCH_CONTAINER`, `SEARCH_NETWORK` and `SEARCH_VOLUME`
can select another installation, such as `MANAGER_CONTAINER=pax-hk-manager`.
Changing those names does not migrate an existing index volume.

On Linux, verify `sysctl vm.max_map_count` before starting ES. It must be at
least 1048576; if lower, persist the required host configuration before starting
the helper. See [Elastic's system configuration](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/vm-max-map-count).

### Current direct Release deployment

The direct Release executor copies the active Manager's complete environment,
HostConfig and network endpoints when replacing it. Consequently, once search
is enabled on the active Manager, later releases retain its search configuration
and private network. This executor does not read a newly edited env file.

Use a Manager build that includes workspace search and applies db/search.sql.
A direct unauthenticated POST to /api/v1/user/self/search should return 401;
404 means that the running binary lacks the route. Matching db/init.sql files
do not establish matching application binaries. Upgrade an older Manager through
normal registered-image Release deployment; the configuration helper deliberately
keeps the current immutable image.

First activation needs one controlled replacement of the current Manager using
the same immutable image. Pause the host's deployment timer, let an in-flight
service finish, and reconcile any blocked task using the supported
`pax-release deploy ... recover-agent` command with that host's original config
and state. Do not delete or rewrite release journals. The activation helper
acquires `agent.lock` and refuses unresolved release work.

For the current US user service, the paths are:

```bash
systemctl --user stop pax-deploy-agent.timer
# If the previous release is blocked, reconcile it before continuing:
# ~/.local/bin/pax-release deploy --config ~/.config/pax-deploy/agent.json \
#   recover-agent --state ~/.local/share/pax-deploy

deploy/vm/search.sh start
python3 deploy/vm/configure_search.py activate \
  --manager pax-manager \
  --agent-state ~/.local/share/pax-deploy \
  --state-dir ~/.local/share/pax-search-activation-$(date -u +%Y%m%dT%H%M%SZ) \
  --search-url http://pax-search:9200 \
  --index pax-search-v1 \
  --health-timeout 240
systemctl --user start pax-deploy-agent.timer
```

Use the actual host's service scope and paths rather than copying US paths to a
different target. Keep the deployment timer paused through activation or rollback;
restart it after checking the active Manager and local 200/401 health probes.
The longer first-activation timeout accommodates existing startup migrations;
removing ES will not repair an unrelated schema initialization timeout.

The helper changes only the search URL, index and persistent read-receipt secret.
It reuses a valid existing secret or generates one without printing it. It keeps
the original container, all original network settings and a private state.json
inside the selected mode-0700 directory. The snapshot contains credentials and
must remain private. Save the three search variables from that private state
into the host's protected environment source for future manual rebuilds; do not
paste its contents into logs or a PR. The US source is
`~/pax_workspace/.secrets/pax-manager.env`.

On activation failure, the helper attempts to restore the exact previous
container. To recover an interrupted operation or explicitly roll back, use the
same saved directory:

```bash
python3 deploy/vm/configure_search.py rollback \
  --agent-state ~/.local/share/pax-deploy \
  --state-dir <the-original-activation-directory> \
  --health-timeout 240
```

Recovery refuses a foreign active Manager. It retains failed containers and never
deletes the ES volume or release state. If host env files or agent required_env
guards were also updated, restore their matching private backups before restarting
the deployment timer after rollback. Once a subsequent application release has
replaced Manager, use that release's recovery workflow; the first-activation
helper correctly refuses the newer container. After a successful activation, the
background worker creates the index and backfills historical sources. Check
indexing progress before expecting every historical search result.

### Older GCloud VM launcher

The historical `deploy/vm/deploy.sh` reads `/etc/pax-manager/env`. For hosts still
using that launcher, provision the variables below and reconnect the recreated
Manager with `deploy/vm/search.sh attach-manager` after each deployment:

```text
SEARCH_URL=http://pax-search:9200
SEARCH_INDEX=pax-search-v1
SEARCH_READ_RECEIPT_SECRET=<a-persistent-random-secret-of-at-least-32-bytes>
```

Alternatively configure that launcher's Docker command to join the network
permanently. Do not use the historical launcher in place of the current Release
executor. To disable search, remove `SEARCH_URL` from the Manager's active runtime
through the host's controlled configuration process, then stop the ES container.

Security is disabled only for the private single-host container network. Do not
publish this service to an untrusted network. For an external cluster enable TLS
and configure an API key. All client queries include an authenticated owner filter;
Manager must still recheck current PG visibility before returning source content.

### Deployment helper tests

```bash
python3 -m unittest discover -s tools -p 'test_vm_search.py' -v
PYTHONPATH=deploy/vm python3 -m coverage run --branch --source=configure_search \
  -m unittest discover -s tools -p 'test_configure_search.py' -v
python3 -m coverage report --show-missing --fail-under=80
```

CI runs both helper suites and requires at least 80 percent coverage for the
activation module. Tests use temporary state and fake Docker adapters. A real
Docker smoke test must also check first activation, exact rollback identity,
static addresses, private ports, and ES data surviving a restart before a host's
first rollout.

## Operational behavior

Documents use stable IDs and strictly increasing external source versions. A
removed source is stored as a persistent `deleted=true` tombstone without body or
title, so a delayed older update cannot resurrect it. Do not manually delete these
tombstones. ES is a derived store and may be rebuilt from PG. Startup backfill inserts only
missing queue rows; it does not reset previously completed work. To rebuild an
index, stop the Manager worker, select a new `SEARCH_INDEX`, and explicitly
requeue all rows before restarting Manager:

```sql
UPDATE search_sync_state
SET desired_revision = desired_revision + 1,
    indexed_revision = 0,
    available_at = NOW(),
    lease_token = '', lease_until = NULL,
    attempts = 0, last_error = '';
```

This also sends retained tombstones into the new index. Keep the old index until
the new one is populated and checked. Do not delete or replace an active index
while workers are running.

Wildcard fields provide literal contiguous substring matching. `*`, `?`, and
backslash in user input are escaped as text. One-character queries remain valid
but can scan many candidates; limit requests and measure latency for real data.
Pagination uses a point-in-time snapshot and `search_after`. Cursors expire after
one minute of inactivity; clients should restart a search after expiration.
Index snapshots can briefly reflect older source state, so API authorization and
version-aware reads remain authoritative.

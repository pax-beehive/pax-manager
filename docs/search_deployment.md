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

`deploy/vm/search.sh start` starts a persistent container on a private Docker
network with no published host ports and attaches an existing `pax-manager`
container to it. This is an operator-run helper; application deployment does not
run it automatically. Add to `/etc/pax-manager/env`:

```text
SEARCH_URL=http://pax-search:9200
SEARCH_INDEX=pax-search-v1
SEARCH_READ_RECEIPT_SECRET=<a-persistent-random-secret-of-at-least-32-bytes>
```

Run the existing Manager deployment, then run `deploy/vm/search.sh attach-manager`
to reconnect the newly created Manager container to the search network. The worker
will reconnect and retry outstanding work. Alternatively configure the Manager's
Docker launch to join this network permanently. `search.sh stop` stops search and
retains its volume; remove `SEARCH_URL` and redeploy Manager to disable search.

Security is disabled only for the private single-host container network. Do not
publish this service to an untrusted network. For an external cluster enable TLS
and configure an API key. All client queries include an authenticated owner filter;
Manager must still recheck current PG visibility before returning source content.

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

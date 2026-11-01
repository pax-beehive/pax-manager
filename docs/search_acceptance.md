# Search MVP acceptance

These checks exercise backend APIs and the standalone Secretary MCP adapter.
They do not deploy the Manager to production.

With Docker running, Go and Python 3 installed, run from this checkout:

```bash
make search-acceptance
```

The command starts real PostgreSQL and Elasticsearch with a unique Compose
project, random loopback ports, and disposable volumes. It never uses your normal
`DATABASE_URL`, test URL overrides, development database, or production data.
It waits for both services to be healthy and treats skipped acceptance tests as
failures. The first run may download the container images and Go dependencies.
An uncached Manager Docker build needs the same private-module access as the
normal Dockerfile: provide `PAX_BEEHIVE_READ_TOKEN` through the environment if
required. The builder consumes it as an optional BuildKit secret.

Expected checks:

| Check | Expected behavior |
| --- | --- |
| Multipart message | Ordered parts form one body; repartitioning preserves its hash |
| Keyword search | Whitespace splits literal terms; more matched terms rank first, then boundary count and newest; Chinese substrings and one-character terms still match |
| Incremental indexing | Late parts update the same message; concurrent changes remain pending |
| Authorization | Another user receives no source content or snippets |
| Summary history | v1 keeps its exact source after editing the original and publishing v2 |
| Idempotency | Concurrent identical requests publish exactly one summary version |
| Deletion | PG revalidation hides deleted sources and dependent summaries even before ES catches up |
| MCP | A real stdio child calls search/read/expand/publish against the actual Manager HTTP server |
| Evidence | Forged read receipts and quotes absent from the read view are rejected |

Success prints `PASS` lines from both stages. Detailed Go JSON, MCP and live
service logs are stored in the temporary directories printed by the command.
Failure exits nonzero. Exit traps remove only that run's containers and volumes,
including on failure or interruption.
Live-runner guard tests and actual HTTP/MCP execution also produce merged
standard-library trace counts and annotated files under the printed log
directory's `coverage.dat` and `coverage/` paths.

The main history scenario is
`TestWorkspaceMVPIntegrationGivenSourceChangesThenSearchAndSummaryHistoryStayConsistent`.
The first stage uses real PG/ES with the business service and focused HTTP/MCP
tests. The second stage builds the actual Manager Dockerfile and starts Manager,
PostgreSQL 16 and ES in a fresh Compose project. The Manager starts its normal
background worker; the test never invokes a worker directly or refreshes ES.
It creates local user API keys through HTTP, seeds only its isolated database,
and sends all search/read/publish operations over HTTP and the genuine stdio MCP
adapter. Neither API keys nor read receipts are printed. This does not measure
production latency.

Run only the full-network stage with:

```bash
make search-live-acceptance
```

## Interactive local stack

```bash
make search-live-up
```

This builds and leaves an isolated stack running, seeds messages and a summary,
and prints its loopback Manager URL, Compose project and exact cleanup command.
The default local identity is `local@example.local`; the second identity has a
separate private message. PostgreSQL and ES have no published host ports.
The local-only auth configuration never changes production defaults. Artifact
operations are outside this search fixture; no object-storage service is started.

Point a local Console proxy at the printed Manager URL to inspect its fixtures.
Stop only that stack using the printed project:

```bash
make search-live-down SEARCH_LIVE_PROJECT=pax-search-live-<printed-project>
```

For interactive API/MCP use, see [search_contract.md](search_contract.md),
[search_deployment.md](search_deployment.md), and
[secretary_search_mcp.md](secretary_search_mcp.md).

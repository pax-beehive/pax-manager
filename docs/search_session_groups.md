# Session-grouped workspace search

The browser can request one card per session without changing the raw document
search used by Secretary tools.

```json
{"query":"port","group_by":"session","limit":20}
```

Send this body to `POST /api/v1/user/self/search`. The additive response is:

```json
{
  "hits": [],
  "groups": [{
    "key": "session:sha256:...",
    "title": "Current custom session name",
    "agent_id": "agent_...",
    "session_id": "sess_...",
    "matches": [{
      "ref": {"kind":"message","id":"message_..."},
      "snippet": "...port...",
      "agent_id": "agent_...",
      "session_id": "sess_..."
    }],
    "matches_has_more": true
  }],
  "next_cursor": "opaque-group-cursor"
}
```

The limit defaults to 20 groups and accepts 1 through 100. Each group initially
contains up to three current, authorized matching objects, ordered by the same
best-document relevance/time rules as raw search. The best candidate sets the
group order; match count does not boost a session. A group's current title may
not contain the query. PostgreSQL supplies the current custom name before the
reported name, falling back to the actual session ID for untitled sessions.

The key includes authenticated owner, agent and session identity. Bare session
IDs are insufficient because different agents may have the same native ID.
Secretary's deterministic session summary joins its explicit primary source
session. Its coverage can include related sessions and is never used to guess
the group. Project/topic/direction summaries without that primary binding remain
standalone cards with document keys and may omit agent/session fields.

## Expanding matches and reading sources

Request further matches with the original query/filters and the exact group key:

```json
{"query":"port","group_key":"session:sha256:...","limit":20}
```

This returns the existing raw `hits` response and its own raw cursor. Preserve
the original `kinds` and `session_ids` filters. The first expansion page can
repeat the initial previews; replace or merge by exact source reference. Do not
reuse a group-list cursor for expansion. Combining `group_by` with `group_key`
is rejected. Existing requests without either field keep their raw behavior,
including the existing raw limit cap of 50 and the Secretary MCP contract.

Use the unchanged `search/read` endpoint for pinned summary/message references
and full source content. Every returned match is re-read from PostgreSQL before
its snippet/ref is returned. Current owner, deletion, archive, hidden-message and
source-version checks remain authoritative. ES source bodies, titles and session
ownership never authorize a result. Grouped ES requests and exact-group expansion
fetch identity/version metadata only; source text comes from PostgreSQL.

`matches_has_more` means more indexed candidates may exist, not an authorized
match count. Further candidates can disappear during permission revalidation.
Only the best four indexed candidates are inspected for each initial group, so
an entirely stale preview window can temporarily hide a group until indexing
catches up. No document total is presented as a session total.

## Pagination and bounds

Elasticsearch field collapse selects groups; bounded `inner_hits` select previews.
Group pages use a separate owner/query/mode-bound PIT plus offset cursor because
collapse with the current multi-field relevance sort cannot use `search_after`.
Raw search keeps its existing PIT/search-after cursor. PIT retention is one
minute and the most recent PIT ID is carried into each continuation.

The service fills removed groups by scanning at most the requested limit plus
100 candidates and applies an eight-second deadline. An incomplete but valid
page can retain a next cursor, including an empty page after visibility filtering.
The default ES result window bounds grouped pagination to 10,000 groups. At that
boundary `pagination_limited:true` is returned without a next cursor; narrow the
query to continue. Current PostgreSQL permissions still apply across the indexed
PIT snapshot. Permission/source changes can therefore remove results mid-query.

## Explicit deployment migration

Deploy the new Manager before enabling the grouped Console experience. Index
initialization adds a `group_key` keyword mapping to an existing strict index;
it does not scan or rewrite historical documents at startup. New writes contain
the key immediately. A grouped query checks whether the authenticated owner's
nondeleted ES documents still lack this field. If so, it returns HTTP 503 with a
migration-required message, instead of silently hiding those documents or
collapsing them into a missing-key bucket. Raw search continues to work.

Run the explicit bounded queue migration using normal libpq environment variables
or `.pgpass`; do not put credentials in command-line arguments:

```bash
PGHOST=<host> PGPORT=<port> PGUSER=<user> PGDATABASE=<database> \
  python3 tools/backfill_search_groups.py --batch-size 500 --max-pages 1
```

Each output line is a JSON checkpoint with `queued`, `after_kind` and
`after_source_id`. Resume using those exact fields:

```bash
python3 tools/backfill_search_groups.py --batch-size 500 --max-pages 1 \
  --after-kind message --after-source-id '<last source ID>'
```

One invocation queues only one page by default. Each page locks at most 500 rows,
has an eight-second statement timeout and a one-second lock timeout. A lock or
statement failure rolls back that page; retry the same checkpoint. No rows are
skipped while advancing a checkpoint. `--batch-size` accepts up to 1,000 and
`--max-pages` can explicitly process additional pages. Connection settings and
existing `PGOPTIONS` are preserved; the timeout bounds are appended.

The migration increments durable desired revisions above both desired/indexed
versions. It preserves active lease tokens/deadlines, so a worker completing an
older revision leaves the new one pending. Existing pending work retains its
earlier availability time; previously clean rows enter at the current time and
do not jump ahead using ancient timestamps. Normal workers re-read current PG
sources and rebuild all document kinds, including primary-summary association.
No model calls or Secretary jobs are created. Re-running an earlier checkpoint
is safe but queues those projections again.

`queued` is not an indexed count. Allow the search worker to drain the enqueued
revisions before grouped readiness succeeds. Existing source rows missing from
the normal sync queue must first be populated by the established search backfill.
No new production mutation is performed merely by deploying this feature.

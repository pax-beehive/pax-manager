# Administrator summary inspector

The Console developer page at `/admin/summaries` uses PostgreSQL-backed list and
detail endpoints to inspect persisted summaries without entering a search query.
Elasticsearch and the indexing worker are not required. The page does not create
summaries or schedule Secretary work.

Both endpoints resolve the current authenticated principal and require
`IsAdmin`. Administrative status does not widen the data scope: an administrator
can inspect only their own summaries. No owner selector or cross-user override is
provided. The backend enforces this restriction independently of Console page
visibility. Valid requests from an authenticated non-administrator return 403.

## Endpoints

Responses use the normal Manager envelope; the objects below appear in `data`.
Normal user authentication is required. The `user_id` path segment does not select
the authorization identity; clients should use `self`.

| Method | Path | Query |
| --- | --- | --- |
| GET | `/api/v1/user/self/developer/summaries` | Optional `limit` and `cursor` |
| GET | `/api/v1/user/self/developer/summaries/:summary_id` | Optional `revision` |

The list returns the latest stored revision for each logical summary ID, ordered
by summary ID ascending. The HTTP limit defaults to 20 and accepts integers from
1 through 100. Duplicate and unknown query parameters are rejected with 400.
The cursor is opaque and owner-bound. An invalid cursor returns 400. Summary
identity and cursor lengths follow the existing publication and HTTP transport
contracts; the browser adds no independent 512-byte ID or 2,048-byte cursor cap.
This cursor is unrelated to search pagination or Secretary progress.

```json
{
  "items": [
    {
      "summary_id": "summary_example",
      "revision": 2,
      "subject_type": "fragment",
      "subject_id": "secretary:session:123",
      "title": "Session progress",
      "body_preview": "Decisions and remaining work.",
      "claim_count": 2,
      "coverage_count": 10,
      "summary_source_count": 0,
      "created_at": "2026-10-31T20:00:00Z"
    }
  ],
  "next_cursor": "opaque-cursor"
}
```

`body_preview` contains at most 400 Unicode code points. Counts describe the
selected revision's direct claims, covered messages, and pinned child summaries;
they are not recursively expanded totals. `created_at` is the revision's creation
time. The list covers all persisted subject types, including retained legacy
fragment identities, rather than only the canonical automatic rolling summary.

Each candidate is checked using the existing summary source-visibility rules
before its title or body preview is returned. A deleted, archived, or otherwise
inaccessible source hides dependent summaries. A page may contain fewer than the
requested number of items, including zero, while still returning `next_cursor`.
Clients must continue according to the cursor rather than item count. A missing
cursor means the current traversal is complete. Concurrent new IDs before an
already consumed cursor are visible after restarting the list.

## Exact revisions and source references

Omit `revision` or use zero to resolve the latest version. A positive integer
selects an exact immutable revision. The response is:

```json
{
  "summary": {
    "summary_id": "summary_example",
    "revision": 2,
    "owner_user_id": "current-user-id",
    "subject_type": "fragment",
    "subject_id": "secretary:session:123",
    "title": "Session progress",
    "body": "The full stored summary text.",
    "coverage": [],
    "created_at": "2026-10-31T20:00:00Z"
  }
}
```

The full `SummaryVersion` can also contain `claims` and `summary_sources`.
Coverage pins message ID, canonicalization version, and content hash. Child
summary references pin summary ID plus revision. The response retains the
existing structure; it does not dynamically substitute newer source versions or
expand every source body. See [search_contract.md](search_contract.md) for the
existing versioned source-read endpoint and snapshot rules.

A missing, foreign-owned, or currently inaccessible summary revision returns
404. Historical versions remain subject to current owner and source visibility
checks. Malformed revision parameters return 400. Unavailable summary storage
returns 503; ordinary database failures retain the normal Manager error handling.

## Storage and deployment

No new table is introduced. PostgreSQL selects current heads from
`summary_versions`, then reuses `getSummaryTx` to load source references and
validate current access in one repeatable-read transaction. An index on
`(owner_user_id, summary_id, revision DESC)` supports the owner-scoped traversal.
The memory store implements the same contract for tests and local development.

Deploy the Manager binary with its complete `db/` directory so normal schema
initialization creates the idempotent browse index. Deploy the matching Console
page for the interface. This feature does not change Secretary settings,
publication, processing checkpoints, summary contents, or board behavior.

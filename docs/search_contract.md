# Workspace search and summary contract

The MVP searches Manager-persisted, owner-visible plaintext messages, session
titles, and current summary versions. PostgreSQL owns source content and access.
Elasticsearch is a disposable derived projection. No embedding, model call, or
Chinese tokenizer is used by search. Summary generation belongs to Secretary.

Administrators can also browse their own current summaries and inspect exact
revisions without ES or a search query. See
[developer_summaries.md](developer_summaries.md) for the separate read-only
developer endpoint contract and current source-visibility rules.

## HTTP entry points

All requests use normal Manager user authentication, including an owner-bound
user API key in `Authorization: Bearer ...`. The `user_id` path segment does not
select an authorization identity. Owner scope is stricter than team visibility.

| Method | Path | Request |
| --- | --- | --- |
| POST | `/api/v1/user/self/search` | `query`, optional `kinds`, `session_ids`, `cursor`, `limit` |
| POST | `/api/v1/user/self/search/read` | `ref`, optional `view`, `offset`, `limit`, `before`, `after` |
| POST | `/api/v1/user/self/search/summaries` | `PublishSummaryRequest` below |

Responses use the normal Manager envelope with the result in `data`. An absent
ES backend returns 503 for search; PG source reads and summary persistence do not
require ES to be healthy. Search queries are literal contiguous substrings,
case insensitive, up to 256 Unicode code points. Limits default to 20 hits and
are capped at 50. Kinds are `message`, `session`, and `summary`.

Search hits contain a typed `ref`, title, plain-text snippet, and applicable
agent/session IDs. They are reloaded and owner/visibility/session-filter checked
against current PG state before any snippet is returned. A stale ES result may
therefore produce a short or empty page with a valid continuation cursor.

Example message reference:

```json
{
  "kind": "message",
  "id": "msg_example",
  "content_hash": "sha256:<64-hex-digits>",
  "canonicalization_version": "visible-text-v1"
}
```

Session references use the stable PG `agent_sessions.id` as a string. Summary
references contain `kind: summary`, `id: <summary_id>`, and an exact `revision`.
Revision 0 explicitly requests current, resolved as the highest stored revision.

## Canonical messages, views, and read receipts

One message becomes one ES document regardless of part count. Visible text parts
are concatenated by `part_index` without invented separators. Raw tool output,
reasoning, replaced invocation rows, and internal Secretary work sessions are
excluded. The canonical full-body
SHA-256 does not change when only part boundaries change. Manager message IDs
identify existing Manager projections; no cross-agent or local-log deduplication
is implied.

User reads default to `full`, assistant reads to `last`. `first` and `last` select
paragraphs of the assembled visible message; they do not mean transport parts.
Every view returns the same full-body version reference, role, time, sequence,
text, available views, and a signed `read_receipt`. `has_more` and `next_offset`
paginate that selected view, with at most 16,384 Unicode code points per page.
Subsequent pages must supply the exact full-body hash and canonicalization
version. Context is bounded to 50 visible messages on each side, omits the anchor,
and renders each message using its default view capped at 2,048 code points,
with its own reference and receipt.

Receipts bind authenticated owner, message ID, canonical version, full-body hash,
selected view, offset, and page limit. They prove the text actually exposed by
this service. Search snippets do not constitute a read receipt.

## Publishing and retaining summary versions

`PublishSummaryRequest` has `summary_id`, `subject_type`, `subject_id`,
`expected_revision`, `request_id`, `title`, `body`, `claims`, `coverage`, and
`summary_sources`. Subject types are `fragment`, `topic`, `direction`, `project`;
automatic Secretary uses a stable `fragment` subject per source session. Logical summary IDs must be globally
unique. Their owner and subject cannot change.

Every coverage element contains `message_id`, `content_hash`,
`canonicalization_version`, and `read_receipt`. Coverage freezes every included
message, not just quoted evidence. Claims may contain evidence with `message`
(a coverage reference), `quote`, optional `view`, and the receipt for the page
that exposed the quote. Quotes must occur literally in that receipt's text and
the claimed message must belong to coverage. There are at most 512 combined
message/child references, 100 claims, 100 child references, and 512 evidence
entries per publication.

Automatic rolling Secretary publication uses a trusted internal path to carry
forward already validated unchanged coverage beyond the public 512-ref limit.
The 4,096-entry dependency guard remains in force. New delta and explicitly read
related sources are verified before composing the complete immutable revision.
The public publication endpoint keeps the limits above.

Child references use `summary_id` plus exact `revision`, never a drifting current
pointer. Access checks fail closed above 4,096 returned dependency entries. Published
versions and their sources are immutable. The existing topic/claim/project model
can attach these references without creating a separate session-summary layer.

Publication verifies receipts, then atomically validates/freezes exact source
bodies, inserts a new summary revision and all source links, and marks that
summary for indexing. The database takes a repeatable snapshot and checks source
ownership again. Reusing the same request ID with the same content returns the
same version; conflicting content or expected revisions returns 409. Receipts
are not persisted as summary facts or included in the idempotency hash.

Only referenced message versions are retained in `message_snapshots`. If a source
changed and its requested old version was never saved, reads/publication return
409 `source version unavailable`. Reread and regenerate the affected summary;
current content is never substituted beneath an old reference. Existing
snapshots remain readable only while the underlying source is currently owned,
active, and visible. Removing/archiving the source hides dependent summaries.

## Incremental indexing

`search_sync_state` records one coalesced row per source. PG triggers mark the
parent message on every relevant part insert, replacement, append, or deletion.
Message/session/owner/node visibility changes also invalidate affected sources
and dependent summaries. Workers claim short-lived leases, read one consistent
source/version snapshot, replace the stable ES document, and acknowledge only
that processed revision. Failures retain work for retry. Deleted documents are
versioned redacted tombstones, not unversioned ES deletes.

The worker starts under the Manager context and can later be moved to another
process using the same interfaces. Startup backfill inserts missing queue IDs
after change capture is enabled, without overwriting newer pending work.

Indexing revision, content hash, summary revision, search cursor, and Secretary
processing checkpoint are separate concepts. Optional automatic generation now
uses the user's explicitly selected agent and saved enable switch; see
[secretary_automation.md](secretary_automation.md). It does not start the stopped
local experimental pipeline. The standalone MCP adapter in
[secretary_search_mcp.md](secretary_search_mcp.md) remains available for explicit
agent-driven reads and publication.

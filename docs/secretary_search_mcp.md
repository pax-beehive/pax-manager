# Secretary search MCP adapter

`tools/secretary_search_mcp.py` is a standalone Python 3 stdio MCP server for
Manager search, source reads, and immutable summary publication. It uses only the
Python standard library. It does not call models, watch sessions, process sources,
modify local Secretary boards, or start the stopped prototype pipeline.

The separate Manager automatic Secretary worker is now configurable from Console
`/settings/secretary`; see [secretary_automation.md](secretary_automation.md).
Its selected-agent execution and checkpoint are independent of this stdio adapter.

## Launch

Configure these environment variables in the MCP client's secret/environment
settings, then launch the script as a stdio child process:

- `PAX_MANAGER_URL`: an explicit native gateway base for your account's region,
  for example `https://paxworkspace.net/api/v1/native/us` or
  `https://paxworkspace.net/api/v1/native/hk`. This path requires the gateway's
  existing `NATIVE_API_ENABLED` policy to be enabled. A trusted direct Manager
  origin or local `http://127.0.0.1:9879` also works.
- `PAX_USER_API_KEY`: an existing regional PAX user API key. The adapter sends it
  as a Bearer header. Do not place it in tool arguments, URLs, committed
  configuration, or logs.

Launch definition, adapting the absolute path to the checkout:

```json
{
  "command": "python3",
  "args": ["/absolute/path/pax-manager/tools/secretary_search_mcp.py"]
}
```

The child must inherit the two environment settings above. The client owns the
stdio connection; starting the script alone in a terminal waits for JSON-RPC input.
HTTPS is required except for loopback development addresses, such as
`http://127.0.0.1:9879`. Redirects are refused so credentials cannot be forwarded.
The adapter exposes no owner/user selection: Manager resolves the authenticated
owner and rechecks PG visibility.

The public origin's ordinary `/api/v1/user/...` paths use browser authentication;
an API key does not turn them into a native route. The adapter accepts only an
origin or an exact `/api/v1/native/us`, `/api/v1/native/hk`, or
`/api/v1/native/client` base, with an optional trailing slash. It preserves that
base when constructing the three search endpoints and rejects arbitrary paths,
encoded path aliases, credentials, query strings and fragments.

The `/api/v1/native/client` base has a different existing authentication contract:
the public and direct regional client gateways require a WorkOS native access
token, not a PAX `paxu_` API key. Use that base only when an external native login
supplies its valid bearer token through the credential environment setting. The
adapter does not perform WorkOS login, refresh tokens or automatically switch
regions. A PAX API key should use the public region-specific `us` or `hk` base,
or a trusted direct Manager origin.

This process can run alongside Secretary's existing local board MCP server.
Register it as a separate server, for example `pax-manager-search`; use the MCP
client's server/tool namespace to distinguish any existing read or expand tools.
Existing local JSON topics/boards are not silently migrated or updated. Only
messages already ingested into Manager are available through this adapter.

## Tools and flow

1. `search({query, kinds?, session_ids?, cursor?, limit?})` finds literal title/body
   matches. Kinds are `message`, `session`, and `summary`. Limits are at most 50.
   Search snippets are leads; use the returned stable `ref` for reads.
2. `read({ref, view?, offset?, limit?, before?, after?})` reads an exact source
   reference. Views are `last`, `first`, and `full`; the backend applies its own
   visible-text rules. Context counts are at most 50 each. Preserve the returned
   version identifiers, offsets, and read receipts. Continue pagination explicitly.
3. `expand({message_id, content_hash, canonicalization_version, view, offset?,
   limit?})` reads `first` or `full` of that message at its expected version.
   It calls the same Manager read endpoint and preserves its response unchanged.
4. `publish_summary({...})` forwards `domain.PublishSummaryRequest` exactly.
   Required fields are `summary_id`, `subject_type`, `subject_id`,
   `expected_revision`, `request_id`, `title`, `body`, and `coverage`.
   Optional fields are `claims` and `summary_sources`.

Publication example (replace all references and receipts with actual read results):

```json
{
  "summary_id": "summary_topic_1",
  "subject_type": "topic",
  "subject_id": "topic_1",
  "expected_revision": 0,
  "request_id": "stable-operation-id",
  "title": "Search decision",
  "body": "Use one Elasticsearch node for the MVP.",
  "coverage": [
    {
      "message_id": "msg_1",
      "content_hash": "sha256:...",
      "canonicalization_version": "visible-text-v1",
      "read_receipt": "receipt-returned-by-read"
    }
  ]
}
```

Coverage contains every covered message version, including those without a claim
quote. Each claim's optional `evidence` contains `message` (a message version ref),
`quote`, optional `view`, and `read_receipt`. Quotes must have appeared in the
specific read receipt. Child summaries use `summary_sources` entries with exact
`summary_id` and positive `revision`, never a floating current pointer.

Retry publication with the same `request_id` and unchanged arguments. For a new
revision use a new request ID and the last known `expected_revision`. Manager
freezes coverage snapshots before publishing. A version conflict/unavailable
source requires reading again and recomputing affected summary content.

The adapter unwraps Manager's API `data` and returns it as both MCP
`structuredContent` and JSON text. It does not rewrite refs, receipts, source text,
or summary versions. API failures are tool errors with safe codes, including
`version_unavailable`, `version_mismatch`, and `version_conflict`. The backend's
arbitrary error text is not exposed. Search pagination is not a processing
checkpoint; no processing loop or automatic pipeline is added here.

## Verification

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools -p 'test_secretary_search_mcp.py' -v
PYTHONDONTWRITEBYTECODE=1 python3 -m trace --count --missing --summary --coverdir /tmp/pax-search-mcp-coverage --module unittest discover -s tools -p 'test_secretary_search_mcp.py'
```

Tests mock HTTP requests and include a real newline-delimited stdio subprocess
smoke test for initialize and tool discovery. They do not call a remote Manager.

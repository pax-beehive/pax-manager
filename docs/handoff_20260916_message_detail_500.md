# Message detail HTTP 500 fix

## Cause and change

Issue 026 fixes GET /api/v1/user/{user_id}/sessions/{session_id}/messages/{message_id}.
This includes a tool message_id copied directly from history with no query options
(the service defaults to output), as well as explicit input/output requests.

GetMessageDetailPage passed integer offset/limit values to
substring(text FROM $4 FOR $5). PostgreSQL inferred both parameters as text,
selecting the regex overload. The real pgx driver failed before executing with:

    failed to encode args[3]: unable to encode 1 into text format for text (OID 25): cannot find encode plan

The storage expression now uses $4::integer and $5::integer. No API, frontend,
paxd, schema, or deployment change is included. The production code change is
one expression and its explanation in storage/message_summary.go.

## Regression and reproduction

message_detail_postgres_test.go uses the actual pgx extended query protocol,
connection-local temporary fixture tables, and the real GetMessageDetailPage.
It covers both sections, JSON reassembly, Unicode/terminal paging, end offsets,
missing input, and cross-agent/session exclusion. The HTTP test additionally
uses the tool message_id returned by history without query parameters.

Run with an isolated PostgreSQL endpoint:

    PAX_MANAGER_MESSAGE_DETAIL_TEST_DATABASE_URL=postgres://... go test -count=1 ./internal/manager/storage -run '^TestPostgresMessageDetailParameterTypesAndPages$' -v

During this task, an isolated in-memory PGlite PostgreSQL engine with its socket
adapter supplied the PostgreSQL wire endpoint. The repository's actual pgx driver
was used. A Go source overlay removing the two casts reproduced the error for
both sections; the unchanged regression then passed with the fix. Each engine
was shut down after its run. Docker stayed stopped; no production records or
schema were accessed. The existing production helper could not reach its
configured GCP project, so the exact deployed instance was not independently
verified. Test tooling stayed under workspace .tools; no npm dependency or lock
file was added to Manager.

Go uses gvm go1.26, GOMAXPROCS=2, GOFLAGS=-p=1, and normal caches. Full Manager
unit tests and native build passed separately from the enabled real-driver
regression. Standard format/lint checks retain the repository's existing findings;
new-code lint is checked separately. No generated source needs regeneration.

Issue 026 is resolved in code. Issue 022 remains partial for the wider history
performance work. Production deployment remains separate from merging this fix.

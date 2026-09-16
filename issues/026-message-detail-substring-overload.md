# Message detail reads select the regex substring overload

**Status:** resolved
**Severity:** high
**Component:** storage / userapi
**Found:** 2026-09-16
**Resolved:** 2026-09-16

## Evidence

GET /api/v1/user/{user_id}/sessions/{session_id}/messages/{message_id}
failed for tool IDs returned by history. Both input and output, including the
no-query-parameter default output request, use the same storage query.

The expression substring(text FROM $4 FOR $5) leaves the last two arguments
untyped. PostgreSQL resolves them to text and selects the regex overload.
The pgx caller supplies Go integers for offset and limit, which cannot be
encoded as these text parameters. A string-encoded probe instead returns NULL
for the synthetic payload, which is also incompatible with scanning into string.
The prior scripted database tests did not invoke PostgreSQL parameter inference.

## Resolution

Cast $4 and $5 to integer to select positional substring. The endpoint contract,
message IDs, authorization, and pagination remain unchanged. A real pgx regression
covers both sections, JSON reconstruction, Unicode/terminal paging, end-of-content,
missing input, and session/agent isolation. The HTTP regression explicitly uses
a history tool's message_id without query parameters.

Validation used an isolated PGlite PostgreSQL engine through its wire-protocol
adapter and the repository's actual pgx driver. No production data was read or
changed. This marks the code defect fixed; production deployment is separate.

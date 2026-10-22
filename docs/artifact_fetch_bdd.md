# User-requested local file fetch: behavior scenarios

These scenarios describe the pax-manager side of "fetch a local file from the
web console". The console calls check, then fetch. The manager relays both to
the paxd node that owns the session over the node-control tunnel. Accepted
fetches reuse the existing artifact publication pipeline (register, prepare,
S3 PUT, complete, failed). Agent publish behavior is unchanged.

Routes (manual Hertz routes in package `manager`):

```text
GET  /api/v1/user/:user_id/sessions/:session_id/artifact-fetches/check?path=<abs>
POST /api/v1/user/:user_id/sessions/:session_id/artifact-fetches   {"path":"<abs>"}
```

## Check

### Scenario: check is allowed

- Given an owned manager-transport session bound to a connected paxd node
- When the user calls check with an absolute path
- Then the manager sends query `artifact_fetch.check` with that path
- And returns HTTP 200 with `allowed:true`, `path`, `filename`, `size_bytes`

### Scenario: check is denied by paxd policy

- Given paxd answers `allowed:false` with a denial `{code, rule, message}`
- When the user calls check
- Then the manager returns HTTP 200 with `allowed:false` and the denial unchanged

### Scenario: paxd returns garbage

- Given paxd answers with a query result that has no `artifact_fetch_check`
  or an inconsistent one
- When the user calls check
- Then the manager returns HTTP 502

## Fetch

### Scenario: fetch is accepted

- Given an owned session bound to a connected paxd node
- When the user posts `{"path":"/home/u/report.pdf"}`
- Then the manager generates `apub_<32 lowercase hex>` and sends command
  `artifact.fetch` with publication_id, agent_id, session_id, path
- And paxd acks `ok:true,status:applied` with filename and size_bytes
- Then a publication row exists with origin `user_fetch`, status `queued`,
  the filename from the ack, an empty title, `requested_by_user_id` set to the
  principal, and `requested_path` set to the requested path
- And the response is HTTP 202 with `accepted:true` and the publication JSON

### Scenario: fetch is denied by paxd policy

- Given paxd acks `ok:false,status:rejected` with `error{code,message,target}`
- When the user posts a fetch
- Then the response is HTTP 200 with `accepted:false` and
  `denial{code, rule:<target>, message}`
- And no publication row is created

### Scenario: the paxd worker registers before the manager

- Given paxd acks the fetch and its worker registers the publication through
  `PUT /api/v1/node/artifact-publications/:pid` before the manager upserts it
- When the manager upserts the same publication and records the audit fields
- Then the request still returns 202
- And the stored row has origin `user_fetch` and the audit fields

### Scenario: the paxd worker registers after the manager

- Given the manager already created the user_fetch publication
- When the paxd worker registers the same publication id with the same source
- Then the PUT returns 200 with the existing publication
- And origin stays `user_fetch`

## Errors

### Scenario: invalid path

- Given a missing, relative, longer than 8192 bytes, or NUL/CR/LF-containing
  path, or a non-JSON body
- When the user calls check or fetch
- Then the response is HTTP 400 and nothing is sent to paxd

### Scenario: session is not owned

- Given the session belongs to another user (or does not exist)
- When the user calls check or fetch
- Then the response is HTTP 404

### Scenario: session is end-to-end encrypted

- Given the session transport is `e2ee`
- When the user calls check or fetch
- Then the response is HTTP 409 and nothing is sent to paxd

### Scenario: node is offline

- Given no node-control tunnel is connected for the session node
- When the user calls check or fetch
- Then the response is HTTP 503

### Scenario: paxd does not answer in time

- Given the node-control call exceeds the bounded timeout (about 10s)
- Then the response is HTTP 504

## Transcript

### Scenario: reconcile ignores user_fetch publications

- Given a user_fetch publication in a session
- And a completed tool call in the same session whose output references that
  publication id
- When artifact display reconciliation runs for the session
- Then no `pax:artifact` display message is created

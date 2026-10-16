# Direct regional gateway

This opt-in Node service runs next to each region's Manager and Console. Browser
REST, streaming, WebSocket and artifact downloads use that regional HTTPS origin.
WorkOS authenticates the user; a local PostgreSQL directory resolves the fixed
home region. No Cloudflare Worker, Access service token or D1 query is required
on this path. Existing Worker deployments remain a separate legacy entrypoint.

## Directory and request flow

Both databases hold the complete minimal directory: WorkOS subject bindings,
canonical identities, original PAX user IDs and immutable home regions. Project,
session and device data stay in the assigned Manager. US is the example authority;
either region can be configured as the single authority before initialization.

- Known accounts read only local PostgreSQL, including during peer outages.
- Registration and first identity binding are committed by one authority. Unique
  constraints and a transaction lock select one account under concurrent requests.
- Each authority transaction appends a durable journal record. The replica polls
  every five seconds and atomically applies each page with its persisted cursor.
  Sequence allocation is serialized with commit, so a poll cannot skip a pending
  transaction. Restart resumes the cursor; rollback never advances it.
- Local misses fetch and persist the authority's record. This does not advance
  the background cursor. An unavailable authority fails discovery/registration
  with 503; absence never implies a different region or permission to create a
  second account. There is no automatic authority promotion.
- Bootstrap provisions only the home Manager, idempotently. On a foreign region
  it confirms provisioning through a signed peer call, then returns the home
  origin. Ordinary foreign business requests return 409 `region_mismatch` with
  the configured `origin` and `api_url`; they are never forwarded across regions.
- Local business requests carry the existing short-lived Manager HMAC assertion.
  Only the local provisioning secret is available to each gateway. Client-supplied
  identity, CF credentials and authorization headers are stripped from that hop.
- Peer RPC uses a separate shared HMAC secret, a fixed purpose/method/path, signed
  body, HTTPS and a short timestamp window. Only trusted peers can access records.
  Keep clocks synchronized. Restrict peer RPC at the network layer where practical.

Authentication still uses WorkOS for sign-in, refresh and key discovery. Browser
cookies are host-only; moving to another hostname can require its WorkOS sign-in
round trip. Cookies or bearer tokens are never put into redirect URLs. The browser
stores only a region hint; every destination still authenticates and validates
its local directory. A returned-account navigation bypasses stale hints to avoid
loops when switching accounts. Fresh accounts explicitly choose US or HK once;
existing accounts do not run region probes or periodic reassignment.

Native bearer bootstrap returns the fixed `api_url` and preserves the existing
user-session model. A native client must persist that URL and handle 409 region
mismatch without replaying mutations automatically. This repository does not
contain the Android application; verify its URL persistence before native rollout.
The legacy native node-enrollment endpoints are deliberately not exposed here.
CLI browser confirmation is supported for the local home region. Cross-region
admin operations and central aggregate analytics remain on their existing
administrative entrypoints; this gateway never signs as a foreign Manager.

## Build and local validation

Use Node 24+ and PostgreSQL 17. From `workers/region-directory`:

```sh
npm ci
npm run typecheck
DIRECTORY_TEST_DATABASE_URL=postgresql://postgres:password@127.0.0.1:5432/directory_test npm test
DIRECTORY_TEST_DATABASE_URL=postgresql://postgres:password@127.0.0.1:5432/directory_test npm run test:regional
npm run build:regional
node --env-file=/secure/hk-gateway.env dist-regional/gateway.mjs
```

Database tests create and remove unique schemas. Use a dedicated test database;
CI provisions an isolated PostgreSQL service. The dedicated regional coverage
check includes the executable entrypoint in its aggregate 80% threshold. Test
HTTP and WebSocket servers bind only ephemeral loopback ports.

Alternatively build `docker build -f Dockerfile.regional -t pax-regional-gateway .`.
Bind the gateway container port to loopback and use private hostnames for Manager,
Console and PostgreSQL. The Docker image does not publish automatically with the
Manager image. Run one gateway per region initially; replication is idempotent,
but this change does not provide a distributed rate limiter or authority failover.

## Initial migration and cutover

1. Prepare both regional Console and Manager deployments. Preserve existing
   Manager databases and user IDs. Set each Manager's `PAX_REGION` and its existing
   `REGION_PROVISIONING_SECRET`. Keep Cloudflare Access validation enabled: signed
   gateway requests already use the Manager assertion branch. Do not expose the
   Manager or PostgreSQL listener publicly. Only Caddy's HTTP/HTTPS ports are public.
2. Configure both HTTPS hostnames as DNS-only records pointing at their EC2s;
   use `Caddyfile.example` with the real hostname. Certificate issuance requires
   reachable ports and working DNS. Add both `/auth/callback` URLs and logout
   return origins to WorkOS. The gateway validates browser origins against its configured regional URL.
3. Freeze **all** old D1 writers (bootstrap and new WorkOS bindings), including
   native and browser entrypoints. This code does not dual-write to D1. Keep the
   freeze until the new authority is the sole writer; concurrent cutover writers
   can allocate conflicting accounts. Take backups of D1 and both Manager DBs.
4. Export the two D1 tables to secure files, without logging account data:

   ```sh
   npx wrangler d1 execute pax-user-regions --config wrangler.browser.jsonc --remote --command 'SELECT user_id,identity_key,region FROM user_regions' --json > /secure/routes.json
   npx wrangler d1 execute pax-user-regions --config wrangler.browser.jsonc --remote --command 'SELECT workos_user_id,identity_key FROM workos_identities' --json > /secure/bindings.json
   node --env-file=/secure/us-gateway.env dist-regional/gateway.mjs import-d1 /secure/routes.json /secure/bindings.json
   ```

   The import accepts exactly one successful result per export, validates every
   row, keeps original identifiers, rejects conflicts and journals imported rows.
   A prepared `{ "routes": [...], "bindings": [...] }` file can instead be passed
   to `import /secure/snapshot.json`. Import runs only on the configured authority
   and does not require `REGIONAL_DIRECTORY_READY=true`.

5. Set readiness true and start the authority privately, then the replica. Keep
   public traffic blocked during verification. Check the counts **and sorted rows**
   of `pax_account_routes` and `pax_account_bindings` against the exports and each
   other. Verify the replica cursor catches the authority journal maximum; logs
   report failed syncs. Back up both directories as durable identity data.
6. Exercise an existing US account through HK and an existing HK account through
   US: each must reach its original user ID and home region. Test WorkOS login,
   refresh/logout, one new account, CLI approval, WebSocket chat, SSE and a Unicode
   artifact download. Stop peer connectivity: a provisioned local account must
   still work, while an unknown account must fail without creating local data.
7. Enable the direct Console hostname(s), disable old registrations permanently,
   and move the main entry hostname to the chosen direct gateway if desired.
   `REGIONAL_ENTRY_ORIGIN` designates which configured regional origin may use the
   remembered browser hint. Bookmarking the home URL avoids the entry hop entirely.
   Returning via a different hostname/storage partition may require one directory
   lookup and sign-in; DNS does not know an individual user's fixed home region.

Do not roll back to an old D1 snapshot after allowing new registrations. Freeze
new writes and reconcile the authority's new rows back into the old directory
before switching back. There is no automatic account movement, deletion or
journal compaction in this version. Monitor directory storage and sync failures;
add a separately reviewed migration for account deletion or regional moves.

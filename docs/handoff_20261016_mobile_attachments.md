# Mobile attachment content routes

## Change

Native Android can render received ACP image/resource blocks and published file
cards. Extend the native gateway with three GET-only content-ticket routes:

- user/self/attachments/:id/content (ticket=1)
- user/self/artifacts/:id/content/:ref
- user/self/artifact-publications/:id/content/:ref

No Manager or paxd implementation changes. No upload, write or arbitrary remote
filesystem routes. WorkOS JWT verification and the signed user assertion remain
unchanged; Manager's existing owner/resource checks remain authoritative.

## Flow

Agent calls existing paxd publish_artifact MCP tool. Android receives its
pax_artifact_publication marker or durable artifact part, requests a content
ticket through the authenticated native gateway, validates the session identity,
and downloads the signed HTTPS URL without forwarding API credentials. Pending
publication is retryable. The client never follows file-download redirects.

Client implementation and validation: sibling pax-clients,
docs/android-attachments.md. This gateway-only change has no generated code or
IDL changes and requires no make generate or Manager service rollout.

## Validation and deployment

- npm run typecheck: passed.
- npm test: 176 tests passed (17 suites); includes dry-run build and coverage.
- Production-config wrangler dry-run: passed.
- Deployed with wrangler deploy --config wrangler.browser.jsonc --keep-vars.
- Active version at 100%: d80db30f-1e37-4ffa-aff0-490100740e8c.
- Previous version: 66f00825-0e97-4b3f-ada3-2b94b2a34172.
- Live curl probes: three anonymous content GETs return 401; content POST and
  other-user path return 404. No authenticated user files were fetched.
- A Python urllib anonymous probe returned 403 at the edge; curl reached the
  gateway and returned its JSON unauthorized response.

The validated source changes are included in the main submission. The worker source
was compared with fetched origin/main before deployment; only these three
content route patterns and their tests differed. No tracked local issue was
changed by this feature.

# Image build and Release registration

The Publish image workflow builds linux/amd64 from the exact main commit,
pushes ghcr.io/pax-beehive/pax-manager:git-<commit>, pulls the immutable digest,
checks its platform and revision label, then registers that digest with
https://release.paxworkspace.net/api/v1/artifacts.

Manager CI calls this workflow after all existing checks, integration tests,
and region-directory checks succeed for a push to main.
The main-only workflow_dispatch entry also supports an explicit first run.
No workflow starts a production deployment.

GitHub's job-scoped GITHUB_TOKEN has packages:write for GHCR publication.
PAX_RELEASE_PUBLISHER_TOKEN is an Actions secret containing the existing
registration-only Release credential. It must never appear in source, logs,
image build arguments, or uploaded workflow artifacts. The existing PAX_BEEHIVE_READ_TOKEN is passed only as a BuildKit secret
for private Go module downloads.

The registration record is uploaded as release-image-<commit> before calling
Release. If registration fails, download artifact.json from that run and use
the existing pax-release register-artifact command with that saved record and
a local publisher token file. This retries the same digest without rebuilding.
HTTP redirects, non-200/201 statuses and mismatched acknowledgements fail the job.

A registered image is a build output, not evidence of a production rollout.
Private GHCR packages require read credentials on deployment agents; runner
pull verification does not prove that agent registry credentials are configured.

Validation before enabling: workflow syntax and shell parsing, plus 16 isolated
registration scenarios across both product workflows (201/200, redirect,
unauthorized, server error, wrong digest, non-integer ID, HTML response).
Fixtures checked failure retention and that the credential is never logged.

## Cloudflare machine authentication

Configure Actions Secrets `PAX_RELEASE_CF_CLIENT_ID` and
`PAX_RELEASE_CF_CLIENT_SECRET` for a dedicated per-repository Cloudflare Service
Token authorized by a Service Auth policy on the Release artifact endpoint.
Keep `PAX_RELEASE_PUBLISHER_TOKEN`: it independently authorizes registration in
Release. The Manager CI caller forwards both Access secrets to its reusable
workflow. These credentials are step-local, never Docker/BuildKit inputs.

Both Access secrets absent preserves the current workflow during migration.
A partial, non-printable, or malformed pair fails the preflight before building.
Registration checks again, sends headers through a private stream, refuses
redirects, restricts the response file permissions and deletes it on exit.
Errors never print the upstream body. A failed registration preserves the
immutable artifact.json for retry without rebuilding the image.

Deploy this support and provision the Secrets before removing the matching
machine Bypass rule. Then verify missing/invalid Service Tokens fail at Access,
and valid Access credentials without a publisher token fail at Release.
Rotate the dedicated credentials in Actions Secrets; do not share them with
other repositories or devices. Public installers and browser login policies
remain separate. This code change does not create credentials or edit Access.

Run `python3 .github/tests/test_release_registration.py` for isolated BDD checks
covering configured/absent/partial/malformed credentials, successful/duplicate
registration, redirect, denied/server error, invalid identity and HTML responses.
The fake curl checks header delivery, credential privacy and immutable retry
records without making any network request. A separate workflow runs these
checks on pull requests and changes to the release workflow/tests.

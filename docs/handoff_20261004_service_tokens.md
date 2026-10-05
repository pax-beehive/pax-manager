# Release workflow Service Token support

The image registration workflow sends optional Cloudflare Service Token headers
in addition to the existing publisher bearer token. The CI workflow forwards
both optional secrets to the reusable workflow. Incomplete/malformed pairs fail
before the image build. The HTTPS request refuses redirects and suppresses
upstream response bodies. Artifact identity verification and retry records remain.

Only GitHub workflows, isolated workflow tests, and release documentation changed.
No Go API, generated artifact, database, deployed service or local issue status
changed; no code generation is required. The Actions secrets and Access Service
Auth policy remain operational rollout steps, described in image-release.md.

Validation: actionlint for all changed workflows; Bash parsing and
`python3 .github/tests/test_release_registration.py` passed with fake curl and
fixture credentials. No production upload/registration was performed. Go checks
are not relevant to these workflow-only changes. No existing issue was closed.

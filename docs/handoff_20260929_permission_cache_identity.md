# Permission cache compatibility identity

## Problem and behavior

A paxd version-only upgrade changed `client_profile_hash`, which participated
in the only permission observation key. Claude agents lost their cached native
choices even though unexpired observations existed for the same agent.

Runtime identities now preserve their full `identity_fingerprint` for diagnostics
and separately persist `configuration_fingerprint`. When a schema-v2 report
contains `client_capabilities_hash` and an implementation fingerprint, the new
`config-v1:` fingerprint combines implementation identity, protocol version,
actual client capabilities, command fingerprint, and worker initialize-result
hash. Descriptive paxd client information is excluded. Adapter-provided or
probed harness identity remains part of implementation identity when available.

Observation writes, exact catalog reads, and existing-session permission changes
use the configuration key when available, otherwise the old identity key.

## Legacy observations

For a consistent runtime with no exact observation, catalog display may use the
same agent's latest unexpired legacy observation. It is marked stale, has no
native default, and all native suggestions require confirmation. Profile risk
mappings are not applied to this unverified fallback.

This fallback never authorizes an existing-session permission mutation. Session
creation still validates the choice against live `session/new` results before
applying it, then stores a current observation. Merely reading the catalog does
not relabel or rewrite historical observations.

Known incompatible `config-v1:` observations are not legacy fallback candidates.
Latest negative observations remain authoritative; the query does not search
backward for an older positive catalog. Exact negative and expired observations
retain their existing precedence. Mixed/unknown pools do not use the fallback.

## Storage and rollout

- `db/init.sql` adds `agent_runtime_identities.configuration_fingerprint` with an
  empty default through an idempotent, additive migration.
- Existing observation rows remain unchanged. Memory and PostgreSQL stores both
  provide latest-by-agent reads with deterministic ordering and isolated scope.
- No public HTTP contract, generated API, or frontend change is required.
- Deploy Manager for legacy list display; update paxd as well for stable future
  compatibility keys. Old Manager binaries can ignore the extra report field
  and database column. Retain all existing observation rows for rollback.
- Normal repository deployment procedures still apply. This change does not
  update production binaries or rewrite production data by itself.

## Verification

Passed:

- Manager: `GOCACHE=/tmp/pax-manager-go-cache go test -count=1 -coverprofile=/tmp/pax-manager-permission-coverage.out ./...`.
- paxd: `GOCACHE=/tmp/paxd-go-cache go test -p 2 -count=1 -coverprofile=/tmp/paxd-permission-coverage.out ./...`.
- Both application binaries build successfully.
- Real PostgreSQL migration test:
  `TestPermissionCacheGivenLegacyPostgresSchemaWhenMigratedThenPreservesObservations`.
  It creates an isolated schema, seeds a legacy observation, applies the additive
  migration twice, and verifies both new identity fields and historical data.
- The Claude regression fixture contains the six options from the historical
  claude-air observation. The stale catalog remains displayable; exact new keys
  survive full runtime fingerprint changes.
- Changed-block statement coverage against current main: Manager 63/66 (95.5%),
  paxd 21/22 (95.5%).
  All newly introduced production functions exceed 80% coverage individually.
- Changed-code lint (`--new-from-rev=HEAD`), integration-package lint, and
  `git diff --check` pass.

The first parallel paxd run timed out in the unrelated one-second acpclient
browser-login probe. That package passed independently, and the complete suite
passed with package parallelism limited to two.

After rebasing onto current main, repository-wide `make fmt-check` and
`make lint` both pass. The prior baseline formatting, lint, and runtime-state
integration issues have already been fixed upstream and are not exceptions to
this PR's validation.

The local stock `make integration-test` image build cannot download private
paxkit modules without `PAX_BEEHIVE_READ_TOKEN`. The isolated PostgreSQL migration
and application integration checks were exercised using local cached dependencies.
The current-main Docker integration job runs in GitHub Actions with the repository
secret; its result is available on pax-manager PR #155. The isolated local stack
was removed using `make integration-down`.

No production service or production database was changed. Issue 031 is resolved
in source; rollout is still required.

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
- Changed-block statement coverage: Manager 68/72 (94.4%), paxd 21/22 (95.5%).
  All newly introduced production functions exceed 80% coverage individually.
- Changed-code lint (`--new-from-rev=HEAD`), integration-package lint, and
  `git diff --check` pass.

The first parallel paxd run timed out in the unrelated one-second acpclient
browser-login probe. That package passed independently, and the complete suite
passed with package parallelism limited to two.

Repository-wide `make fmt-check` and `make lint` remain blocked by existing
formatting and nine unrelated lint findings (eight complexity findings and one
unchecked rows.Close). No new lint finding was introduced.

The stock `make integration-test` build cannot download private paxkit modules
without the unavailable `PAX_BEEHIVE_READ_TOKEN`. An equivalent isolated stack
was built from local cached dependencies and used to run integration tests.
`TestACPTunnelRuntimeStateIntegration` also fails on the unmodified HEAD image
with the same missing-running-state assertion. The paxd-container integration
build independently requires the missing private dependency credential.
These two cases prevent claiming a fully green integration suite. All other
integration cases passed on the changed Manager image when those two cases were
explicitly excluded. The isolated stack was removed using `make integration-down`.

No production service or production database was changed. Issue 031 is resolved
in source; rollout is still required.

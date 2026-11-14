# Runtime Configuration and Feature Gates

Each regional Manager (US, HK) stores its own runtime overrides in its own
Postgres database. There is no separate service and the regions do not depend
on each other.

Design contract: `output/config-center/config-center-design.md` (sections 1-6).

## Resolution

Every value resolves in this order and reports its `source`:

1. Database override (`db`)
2. Environment variable (`env`)
3. Code default (`default`)

With no database overrides, Manager behaves exactly as before this change,
except for the feature gates listed below, which default to `admin`.

The registry in `internal/manager/runtimeconfig/registry.go` is the source of
truth. Keys that are not registered are rejected on write and ignored on read.

## Runtime behavior

- Startup builds a snapshot from environment values and defaults, then tries
  to load overrides with a 5 second bound. A failed load never blocks startup:
  Manager logs an error, `/api/v1/health` reports `"config": "degraded"`, and
  the background poller keeps retrying.
- Each instance polls `SELECT revision FROM runtime_config_revision` every 15
  seconds and reloads the table only when the revision changed. The writing
  instance refreshes immediately after a successful write.
- Readers use an `atomic.Pointer` snapshot. Request paths never query the
  database for configuration.
- A failed refresh keeps the last-known-good snapshot and increments
  `refresh_failures` (reported with `last_refresh_at` by
  `GET /api/v1/admin/config`).
- One bad override (undecodable JSON, failed validation, unregistered key) is skipped and logged by key only; that entry falls back to
  env or default and every other override still applies.
- Search and voice read the snapshot on every use. The search client and the
  voice provider are cached by a configuration fingerprint and rebuilt when the
  effective settings change. A new search target has its index ensured before
  the first document is written. Note that switching to a different search
  cluster or index does not re-run the full backfill; use the existing
  backfill tooling when moving to an empty index.

Note: `openStore` still pings `DATABASE_URL` before startup, as before. The
non-blocking guarantee covers the configuration tables, not the main store.

## Feature gates

Value: `{"stage": "off" | "admin" | "beta" | "ga", "beta_emails": [...]}`.

| stage | who can use it |
| --- | --- |
| off | nobody, including administrators |
| admin | `IsAdmin` users |
| beta | administrators plus `beta_emails` (normalized with `domain.NormalizeEmail`) |
| ga | every signed-in user |

| key | default | notes |
| --- | --- | --- |
| feature.secretary | admin | Secretary, including summary browsing |
| feature.notifications | admin | Push notifications |
| feature.collaboration | admin | Teams, invites, friends, envelopes, team memex |
| feature.knowledge_capsules | admin | Knowledge capsules and injections |
| feature.browser_control | admin | Reported to Console only; not gated server-side yet |
| feature.android_control | admin | Reported to Console only; not gated server-side yet |
| feature.voice_input | from `VOICE_ADMIN_ONLY` | `true` (default) means admin, `false` means ga |

Deploying this change makes Secretary, Notifications, Collaboration, and
Knowledge capsules unavailable to ordinary users until an administrator moves
the gate to `beta` or `ga`.

### Server-side gates

`featureRoutes` in `internal/manager/feature_gate.go` returns
`404` with `data.error = "feature_unavailable"` when the caller is not allowed:

- `/api/v1/user/:user_id/secretary` and every route below it (settings, run,
  progress, summaries and revisions, tasks and actions, guidance, operations)
- `PUT /api/v1/user/self/notifications/receivers/web-push`
- `POST /api/v1/user/self/notifications/receivers/:receiver_id/test`
- `/api/v1/user/:user_id/teams/...`, `/team-invites/...`, `/friends/...`,
  `/envelopes/...`
- `/api/v1/user/:user_id/knowledge-capsules/...`,
  `/api/v1/user/:user_id/sessions/:session_id/knowledge-capsules`,
  `/api/v1/user/:user_id/sessions/:session_id/knowledge-injections`

Listing and revoking push receivers stay open so users can always remove a
device. Unauthenticated requests are not gated and keep their 401.

Background jobs:

- The Secretary worker skips jobs whose owner fails
  `Feature(owner, "secretary")`. Skipped jobs stay leased and are not failed;
  one pass spends at most 32 claims on skipped owners.
- The notification worker suppresses deliveries to users without
  `notifications` (`error_category = feature_disabled`).

## Settings that can be changed in the admin UI

| key | type | env var |
| --- | --- | --- |
| search.url | url | SEARCH_URL |
| search.index | string | SEARCH_INDEX (default `pax-search-v1`) |
| voice.provider | enum: openai, qwen, soniox | VOICE_PROVIDER |
| voice.openai.url / .model | url / string | VOICE_OPENAI_URL / VOICE_OPENAI_MODEL |
| voice.openai.delay | enum: minimal, low, medium, high, xhigh | VOICE_OPENAI_DELAY (empty means low) |
| voice.qwen.url / .model / .language | url / string / string | VOICE_QWEN_* |
| voice.soniox.url / .model | url / string | VOICE_SONIOX_* |
| transport.journal_gc_disabled | bool | TRANSPORT_JOURNAL_GC_DISABLED |

All entries apply live. `voice.provider=volcengine|byteplus` and the
`VOICE_SEEDASR_*` variables keep working from the environment but are not
editable in the admin UI.

## Credentials are environment-only

API keys are never stored in the database, never listed by the admin API,
and cannot be overridden (writes return `404 unknown_key`), not even for a
connection test. They are read from the environment exactly as before:
`SEARCH_API_KEY`, `OPENAI_API_KEY`, `DASHSCOPE_API_KEY`, `SONIOX_API_KEY`,
`DEEPSEEK_API_KEY`, and `VOICE_SEEDASR_*` keys. The snapshot carries the
search and voice keys only so that clients rebuild when a non-secret setting
changes; they never leave the process.

A future move of credentials to a managed store (for example AWS Secrets
Manager) is out of scope.

## Environment-only settings

These are never exposed in the admin UI: `DATABASE_URL`, `PAX_REGION`,
`REGION_PROVISIONING_SECRET`, `SECRET_VAULT_MASTER_KEY`, `CLOUDFLARE_ACCESS_*`,
`ADMIN_EMAILS`, `TRUSTED_CLOUDFLARE_PROXY_CIDRS`, `ALLOW_LOCAL_USER_HEADER`,
`OBJECT_STORAGE_*`, `PAX_WEB_PUSH_*`, `REGISTRATION_TOKEN*`, and every
credential listed above. Every other variable not in the admin table (for
example `VOICE_LANGUAGES`, `VOICE_MAX_SESSION_SECONDS`,
`SEARCH_READ_RECEIPT_SECRET`, `TEAM_MEMEX_EXECUTOR`, `DEEPSEEK_*`) also
remains environment-only.

## Storage

Migration: `db/migrations/20261113_runtime_config.sql`, applied idempotently by
`PostgresStore.EnsureSchema` at startup. Tables: `runtime_config`,
`runtime_config_audit`, `runtime_config_revision`.

A write (set, reset, revert) is one transaction: lock and increment the global
revision, check `expected_version` against the row, upsert or delete, append
the audit record. Per-key versions never repeat; after a reset the entry
reports version 0 and the next write continues from the highest audited
version.

## API

Responses use the standard `{data, code, message}` envelope. Errors set
`data.error` to one of `unknown_key`, `validation_failed`, `version_conflict`, `not_admin`,
`feature_unavailable`. (`vault_not_configured` from the contract is unused
because no credential is ever written.) Validation errors
add `data.field_errors`; conflicts add `data.version` (the current version).

Admin routes require `IsAdmin` (403 `not_admin` otherwise) and are in the
`foreignAdminTarget` allowlist, so an administrator from the other region can
call them through the regional browser identity. A non-admin foreign identity
is rejected with 401.

| method | path | notes |
| --- | --- | --- |
| GET | /api/v1/admin/config | region, revision, health, entries (`secret` is always false) |
| PUT | /api/v1/admin/config/{key} | `{value, expected_version, reason}`; 400/404/409 |
| DELETE | /api/v1/admin/config/{key}?expected_version=&reason= | reset to env/default |
| POST | /api/v1/admin/config/test | `{group, overrides}`; 10 s timeout; never writes; only `search` has a tester; uses `SEARCH_API_KEY` |
| GET | /api/v1/admin/config/audit?key=&cursor=&limit= | `{items, next_cursor}`, newest first |
| POST | /api/v1/admin/config/audit/{id}/revert | `{expected_version, reason}` |
| GET | /api/v1/user/self/features[?as=user] | `{revision, features}` booleans only |

`reason` is required and at most 500 characters. `expected_version` is 0 when
the key has no override. "Apply to both regions" is two independent PUTs from
Console and is not atomic.

## Operations

- Health: `GET /api/v1/health` includes `config: "ok" | "degraded"`.
- Emergency disable of a feature: set its stage to `off`; it applies on the
  writing instance immediately and on other instances within 15 seconds.
- Roll back a change: `POST /api/v1/admin/config/audit/{id}/revert`.
- Remove an override entirely: `DELETE /api/v1/admin/config/{key}`.

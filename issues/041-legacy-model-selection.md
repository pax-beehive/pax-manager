# [ISSUE-041] Legacy ACP model catalogs cannot be changed

**Status:** resolved
**Severity:** medium
**Component:** session / userapi / Console
**Found:** 2026-10-19
**Resolved:** 2026-10-19

## Summary

Hermes advertises `models.currentModelId/availableModels` and implements
`session/set_model`, but the Manager only writes standard `configOptions`.
Console therefore renders its model catalog as read-only.

## Expected behavior

When no standard model option exists, expose the advertised legacy catalog as a
selectable REST `model` option. Validate IDs, send `session/set_model`, and persist
the acknowledged selection without replacing the catalog or unrelated options.
Standard options must take precedence and failures must leave the model unchanged.

## Scope

This covers plaintext sessions through the current user configuration API.
It does not add legacy agent API routes, encrypted configuration, reasoning-effort
support, automatic model discovery, or a legacy force-refresh operation.

## Resolution

Manager now projects a legacy catalog as a REST model selector only when no
standard model option is present. Writes validate against the advertised list,
use the existing authenticated session-control route, and persist the model only
after the agent acknowledges `session/set_model`. Empty acknowledgements preserve
the catalog and other configuration values. Agent errors leave storage unchanged.

HTTP/ACP tests cover acknowledged switching, busy/unsupported errors, invalid
values, standard-option precedence, and reading the confirmed model after reload.
The Console selector test verifies that displayed selection waits for confirmation.

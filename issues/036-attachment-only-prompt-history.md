# [ISSUE-036] Attachment-only prompts disappear from history

**Status:** partial
**Severity:** high
**Component:** history / ACP projection
**Found:** 2026-10-06
**Resolved:** -

## Cause

`projectACPUserPromptForSessionTurn` returned without creating a message when
the extracted prompt text was empty. Console permits sending attachments without
text; Manager localizes these as ACP `resource_link` blocks. The command reached
the agent, but the user message was never projected into durable history.
Reopening the session therefore showed assistant replies without the screenshot
that prompted them. This affects plain sessions independently of E2EE key access.

## Current note

The projection now retains prompts containing a nonempty resource link and keeps
the original frame for Console's existing attachment renderer. Empty prompts
remain ignored. Legacy requests without a turn or RPC ID use the raw parameters
as their fallback identity input so distinct attachments do not collapse onto
the hash of empty text. Normal turn and RPC identity precedence is unchanged.

This prevents future omissions after deployment. Production rollout is pending.
Historical backfill is explicitly out of scope at the owner's request. This
change does not modify existing history or replay prompts to agents.

## BDD verification

- Given an attachment-only prompt, when projected and read through summary
  history, then its user role, turn identity and original attachment frame remain.
- Given a retry of that command, then only one history message exists.
- Given distinct attachment-only legacy commands without IDs, then they remain
  distinct while an identical retry is deduplicated.
- Given an empty or malformed prompt, then no empty history row is created.

The attachment cases failed against the original projection with zero rows.

Validation passed with `GOWORK=off`: `make fmt-check lint`,
`go test -count=1 ./...`, and `go build ./cmd/manager`. The ACP history regression
suite covers 18/18 statements in coverage blocks intersecting changed lines;
the new resource-link helper has 100% statement coverage. No production rollout
or historical repair was performed.

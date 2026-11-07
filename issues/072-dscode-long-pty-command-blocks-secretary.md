# ISSUE-072 DSCODE long terminal input can block a Secretary execution

**Status:** partial
**Severity:** high
**Component:** DSCODE runtime / terminal / Secretary
**Found:** 2026-11-05
**Resolved:** -

## Problem

A Secretary execution passed its Pax MCP approvals, then used native bash to
assemble JSON despite the prompt's command prohibition. A roughly 27 KiB Python
heredoc remained inside the terminal tool until the five-minute execution
deadline canceled the turn. The job became unknown and required recovery.

Native records showed the final command waiting about 130 seconds before
AbortError. The generated script only parsed and printed JSON; no loop, stdin
read, sleep, or subprocess explained that delay.

## Reproduction

Independent synthetic PTY processes used the installed DSCODE command wrapper,
an interactive bash without user profiles, and synthetic JSON only. A 1 KiB
command completed with its end marker in about 0.2 seconds. A 27 KiB command
reached the start marker but not the end marker before a bounded test timeout.
Synthetic processes were terminated and reaped. No real session was used.

The native wrapper converts the entire command into one ANSI-C quoted eval
input and sends it in a single terminal write. This reproduces a length-related
input failure; the exact underlying readline or kernel mechanism is unproven.
Splitting that wrapper into short lines but writing them all at once also
failed the synthetic test. A fix must validate delivery and terminal readiness,
not merely transform the string.

## Scope and current note

The installed DSCODE ACP adapter mounts its coding preset unconditionally.
Session creation and deploymentConfig do not expose a tool allowlist; session
config options only select the model and reasoning effort. Adding an imagined
tools flag or relying on the existing prompt is not a hard tool restriction.

A local native compatibility hotfix was tested and applied with an exact source
SHA check, verified backup and atomic replacement. Bounded input lines are sent
only after terminal stdin_read readiness. Twenty-one tests and independent
review passed. A full 26,849-byte command also passed against the installed
native SDK in 8.49 seconds, with matching output, exit 0 and no residual process.

This remains partial because it is an installed-runtime hotfix, not an upstream
DSCODE release. A native upgrade can overwrite it. The Manager source-waiting
display fix is separate. Do not widen automatic approval, infer task success
from idle, or repeatedly resend unknown executions as a workaround. See the
[handoff](../docs/handoff_20261106_054500.md) for validation and restore details.

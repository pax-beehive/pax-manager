# Native clients cannot reach device login behind origin Access

- Status: partial
- Resolved: -
- Severity: high
- Component: Worker / native client / auth

## Problem

Android receives an interactive Access redirect before regional Manager device
login. The public gateway accepts browser sessions or Node Keys, neither of which
is an Android user API key.

## Current note

A disabled-by-default, explicitly allowlisted native gateway is implemented and
passes the full Worker suite (144 tests). Production activation is complete;
Android US/HK start/poll and unauthorized access checks passed. HK confirmation
URLs now use the canonical WorkOS page. Regional Access and Manager key
authorization are preserved. Real-account confirmation and live-agent validation
remain pending. Current Worker version: `e91fd9c2-53ca-40ec-b52e-4abe52ab526a`.
See [handoff](../docs/handoff_20261013_200000.md).

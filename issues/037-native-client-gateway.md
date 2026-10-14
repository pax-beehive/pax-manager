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
passes the full Worker suite (143 tests). Activation and real-account validation
remain pending. Regional Access and Manager key authorization are preserved.
See [handoff](../docs/handoff_20261013_200000.md).

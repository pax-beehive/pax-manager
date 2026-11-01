# ISSUE-046 Legacy container test assumes an active mailbox poller

**Status:** open
**Severity:** medium
**Component:** integration / deployment
**Found:** 2026-10-31
**Resolved:** -

## Summary

The full Docker integration suite's mock Hermes container test waits for a
legacy mailbox round trip that the current paxd runtime does not execute.

## Current behavior

`TestPaxdContainerMockHermesIntegration` successfully registers the node and
agent, starts the paxd container, and observes the agent online. Its
`completes_the_mailbox_round_trip` subtest then times out after 60 seconds at
`integration/paxd_container_integration_test.go:185`.

The failure reproduced with both the Secretary feature's paxd image and an image
built from the unchanged paxd commit `5a7644d`. That baseline's active
`cmdRun -> daemon.Bootstrap` path wires ACP/node supervisors, while the legacy
`internal/poller.PollMailbox` implementation is not wired into that path.
Secretary's read-only MCP changes do not modify mailbox execution.

This is distinct from ISSUE-007's legacy `/api/agent` envelope-decoding problem.
No claim is made that all current ACP messaging is broken: the Secretary
HTTP/ACP/PG and cross-binary MCP gates pass independently.

## Expected behavior

The formal container harness should exercise a supported runtime path and
complete an actual message round trip. If legacy mailbox execution remains a
supported requirement, its runtime wiring needs an explicit implementation and
separate coverage. Do not silently skip the failing test or increase its timeout.

## Verification

- Full isolated Docker suite: 10 top-level tests passed, 1 failed, zero skipped.
- Unchanged paxd baseline probe: setup/registration/online checks passed; the same
  legacy round-trip child and parent failed, zero skipped.
- The isolated compose project and its volumes were removed after both probes.

The Secretary handoff records the exact local log paths and test commands.

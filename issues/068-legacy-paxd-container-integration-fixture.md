# ISSUE-068 Legacy Paxd container fixture uses obsolete bootstrap and Hermes transport

**Status:** open
**Severity:** medium
**Component:** integration / Paxd test harness
**Found:** 2026-11-04
**Resolved:** -

## Problem

`TestPaxdContainerMockHermesIntegration` requires a sibling Paxd checkout and is
skipped by the normal standalone Manager checkout. Forcing it to run against
current Paxd exposed an obsolete fixture: it passes legacy cloud and agent
environment variables to `paxd run` and expects the Hermes HTTP mailbox flow.
Current Paxd uses persisted daemon desired state and ACP processes/tunnels, so
the fixture never registers its intended remote and hosted agent.

## Current note

The summary Nodes change does not modify daemon bootstrap or Hermes transport.
Its new five-tool contract is separately verified using a real Paxd binary over
stdio, the authenticated Manager bridge, ACP execution, and atomic Node
publication. This is not proof that the legacy full chat-container fixture works.

## Remaining

Migrate this fixture to supported local control registration and an ACP mock,
then run it in CI with an explicit matching Paxd revision. Keep normal-checkout
skips visible until that migration is complete. No production runtime behavior
should be added just to restore the obsolete fixture.

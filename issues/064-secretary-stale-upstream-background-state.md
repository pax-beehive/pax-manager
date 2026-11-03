# Upstream background runtime snapshots can block Secretary sources

- Status: open
- Severity: medium
- Component: upstream runtime state / session scheduling
- Resolved: -

## Problem

A read-only production audit during issue 063 found three pending source sessions
that were not eligible for Secretary claims because upstream runtime snapshots
continued reporting a running background turn. None had raw message or part
activity in the prior 24 hours; the latest source activity was on October 31 or
November 1, while the snapshots continued updating on November 3 UTC. All three
had zero queued turns, and two had a last recorded completed turn event.

Those observations do not prove the native agent is idle. Background execution
can legitimately continue without producing a message. A recent runtime snapshot
with an old message timestamp must not be automatically treated as an idle or
abandoned session. No source IDs, account identifiers, node IDs, or message
contents are included here.

## Current note

This is separate from rejected completed Secretary output in issue 063. The
bounded validation retry fix does not bypass the existing idle gate, reset
upstream runtime state, or prompt an agent while an active turn may exist.

A follow-up should reconcile the upstream background-turn lifecycle with
native authoritative state and determine why it remains running after completed
visible work. Any automatic repair needs explicit terminal/idle evidence and
must preserve active background execution. No production state was changed for
these three sources during this audit.

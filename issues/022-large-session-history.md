# Large tool payloads inflate session history reads

**Status:** partial
**Severity:** high
**Component:** userapi / storage
**Found:** 2026-09-16
**Resolved:** -

## Evidence

History loads full message raw_json and all message_parts payload_json even
when Console displays tools collapsed. Tool frames are present twice; prompts
also carry an extracted text copy. The history reconciler additionally scans
up to 1000 messages and can write artifact projections on each GET.

## Current note

The session-scoped history endpoint supports view=summary. Console uses this
mode with 100 base rows per page plus non-tool context for the turns on the page. PostgreSQL projects only tool identity/title/
status/kind and omits tool parts from the list. User prompt frames are retained
once, and canonical prompt text parts remain inline without duplicate payload JSON. Artifact/invocation and
permission display records remain available. Summary reads skip the historical
artifact repair hook; normal publication and ACP write paths still reconcile.

An authorized message detail endpoint returns input or output in bounded
Unicode slices, with a revision required for continuation. Console loads these
only when a tool is opened and exposes explicit Load more and Reload details.

Legacy full history is preserved for compatibility, including its old repair
behavior. Ordinary reply/thought content remains inline and can still be large.
Persisted tool raw JSON is not migrated; PostgreSQL may still detoast a large
value when extracting summary fields. Detail reads of terminal parts assemble
text inside PostgreSQL before slicing. Actual production latency/EXPLAIN has
not been measured. The remaining work is not classified as resolved.

The initial detail endpoint SQL overload failure is tracked and fixed separately
in issue 026. The remaining performance limitations above keep this issue partial.

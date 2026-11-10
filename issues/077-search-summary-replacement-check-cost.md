# ISSUE-077 Search summary source checks repeat display replacement parsing

**Status:** partial
**Severity:** high
**Component:** Search / summary source visibility
**Found:** 2026-11-10
**Resolved:** -

## Problem

Grouped search revalidates each hit against PostgreSQL. A summary hit expands
its source dependencies and validates current canonical visibility. The batch
validator still correlates display replacement checks to every source message,
so messages from the same session repeatedly scan and parse the same display
rows. The existing partial display index is used, but does not remove this
repeated work.

An owner-scoped read-only production probe of three larger current summaries
measured approximately 90, 260 and 1451 ms for the source access SQL, before Go
decoding, network transfer or the rest of the search response. The slow sample
has 409 sources. Its plan scans the display index 409 times and invokes JSON
replacement validation 6953 times. This is a measured hotspot, not a trace of
the user's exact reported request.

The ES node was healthy without a search queue at inspection. Four bounded
generic-term probes reported ES internal query time between 99 and 139 ms.
Those probes do not establish frontend, gateway or complete API latency.

## Required behavior

- Classify relevant display replacements once per selected agent/session scope
  in each source-validation batch.
- Preserve the current array precedence and parent fallback, including null,
  empty and invalid replacement arrays.
- Keep owner, deletion, archival, internal-session, hidden-content, canonical
  message and part validation. No permission cache survives the transaction.
- Keep single-message reads and public search results compatible.
- Do not change production data, deploy an index, or bypass source checks to
  reduce latency.

## Current note

The focused batch SQL correction is implemented. Real PG TDD demonstrates
3072 array-processing loops before the change and 48 after it for the same
128-source/24-display fixture. The changed validator has 88.9% coverage.
Owner-scoped same-snapshot production SELECT/EXPLAIN probes measured
89.700 / 269.399 / 1561.589 ms before and 48.739 / 48.568 / 58.319 ms after;
the second query may benefit from warmed buffers. Three source-set comparisons
reported zero differences. No production code or data was changed.

Full Go tests with isolated PG fixtures, format, normal/integration lint, build,
standard isolated Docker integration and tagged HTTP/ACP/PG acceptance passed.
The implementation is ready for review; production rollout and complete API
latency acceptance remain pending.

Other potential costs remain separately measurable: serial candidate hydration,
repeated group metadata reads, a fourth non-displayed candidate, ES setup
requests and deep-page collapse offsets. This issue does not claim to explain
the user's exact request or remove every source of visible search delay.

# Transport journal retention

PostgreSQL Manager instances automatically prune completed transport history in
the background. No scheduler or manual database operation is required. The worker
waits for its first tick, so cleanup does not hold up HTTP startup. It stops and
joins before the database is closed.

Default policy:

- Retain all records updated within the last 72 hours.
- Keep a diagnostic tail of 100 sequence positions per queue, stream, and direction.
- Delete only outbound `acked` and inbound `applied` frames.
- For outbound frames, stay below the persisted next outbound sequence minus the tail.
- For inbound frames, stay below both persisted ACK and applied watermarks minus the tail.
- Keep missing queue state, pending, sent, received, rejected, and failed records.
- Delete at most 1,000 rows per 10-second tick, with a five-second batch deadline.

Each batch is one PostgreSQL statement. It uses the existing status/time cleanup
index ordering, takes no global maintenance lock, and skips rows in use by another
transaction. Multiple Manager replicas may run cleanup safely. Failures are logged
and retried on the next tick. Successful nonempty batches log the deleted count.
Queue state and application message/history tables are not removed or recomputed.

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `TRANSPORT_JOURNAL_GC_DISABLED` | `false` | Emergency switch to disable automatic cleanup |
| `TRANSPORT_JOURNAL_RETENTION_HOURS` | `72` | Minimum age since the frame was last updated |
| `TRANSPORT_JOURNAL_GC_INTERVAL_SECONDS` | `10` | Delay between cleanup batches |
| `TRANSPORT_JOURNAL_GC_BATCH_SIZE` | `1000` | Maximum rows per batch; upper bound 10,000 |

No environment changes are needed to enable the defaults. Missing, nonpositive,
or malformed numeric environment values use defaults, consistent with other
Manager settings. Batches above 10,000 prevent the worker from starting. Stop GC
with the explicit disable switch rather than an invalid value.

The first rollout still executes the existing startup schema initialization before
the worker can run. This change does not remove its historical migration queries.
Existing large tables drain gradually after Manager is healthy. Check startup and
rollback health timeouts before rollout. Deletion is irreversible; old diagnostic
frames cannot be restored by rolling back the application binary. PostgreSQL
autovacuum can reuse deleted space; it need not immediately shrink the files.

The existing legacy `DeleteCompletedTransportFrames` API is unchanged. The new
worker uses the stricter watermark-aware pruning API, with the same shared
transport journal for current node and legacy agent traffic.

An isolated PostgreSQL test is available under the integration build tag. It requires
`PAX_JOURNAL_GC_TEST_DSN` to point to `127.0.0.1` and database `journal_gc_test`.
Use a disposable database; the test creates its own minimal tables. Do not use a
production database or production Docker volumes.

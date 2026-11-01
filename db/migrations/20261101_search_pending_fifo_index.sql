-- Run as a standalone autocommit statement before deploying the fresh-schema definition.
-- Existing names fail deliberately; verify definition and validity before deciding to skip.
CREATE INDEX CONCURRENTLY idx_search_sync_pending_fifo ON search_sync_state(available_at, kind, source_id)
    WHERE desired_revision > indexed_revision;

-- Run as a standalone autocommit statement before deploying the fresh-schema definition.
-- Existing names fail deliberately; verify definition and validity before deciding to skip.
CREATE INDEX CONCURRENTLY idx_messages_search_display_agent_session ON messages(agent_id, session_id)
    WHERE message_type IN ('pax:invocation', 'pax:invocation_pending', 'pax:artifact');

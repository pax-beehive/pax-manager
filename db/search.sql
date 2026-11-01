CREATE TABLE IF NOT EXISTS search_sync_state (
    kind TEXT NOT NULL CHECK (kind IN ('message', 'session', 'summary')),
    source_id TEXT NOT NULL,
    desired_revision BIGINT NOT NULL DEFAULT 1 CHECK (desired_revision > 0),
    indexed_revision BIGINT NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_until TIMESTAMPTZ,
    lease_token TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(kind, source_id)
);
CREATE INDEX IF NOT EXISTS idx_search_sync_pending ON search_sync_state(available_at)
    WHERE desired_revision > indexed_revision;
CREATE INDEX IF NOT EXISTS idx_search_sync_pending_fifo ON search_sync_state(available_at, kind, source_id)
    WHERE desired_revision > indexed_revision;

CREATE OR REPLACE FUNCTION pax_search_mark_dirty(document_kind TEXT, document_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    dependent_id TEXT;
BEGIN
    IF document_id IS NULL OR document_id = '' THEN RETURN; END IF;
    INSERT INTO search_sync_state(kind, source_id)
    VALUES (document_kind, document_id)
    ON CONFLICT (kind, source_id) DO UPDATE
    SET desired_revision = search_sync_state.desired_revision + 1,
        available_at = NOW();
    IF document_kind = 'message' THEN
        FOR dependent_id IN WITH RECURSIVE affected(summary_id) AS (
            SELECT summary_id FROM summary_sources WHERE message_id = document_id
            UNION
            SELECT parent.summary_id FROM summary_sources parent
            JOIN affected child ON parent.child_summary_id = child.summary_id
        ) SELECT summary_id FROM affected
        LOOP
            PERFORM pax_search_mark_dirty('summary', dependent_id);
        END LOOP;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION pax_search_message_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    row_data JSONB;
    replacement_id TEXT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM pax_search_mark_dirty('message', OLD.message_id);
    END IF;
    IF TG_OP <> 'DELETE' AND (TG_OP = 'INSERT' OR NEW.message_id IS DISTINCT FROM OLD.message_id) THEN
        PERFORM pax_search_mark_dirty('message', NEW.message_id);
    END IF;
    -- Replacement display rows also change whether their original rows are visible.
    FOR row_data IN
        SELECT value FROM jsonb_array_elements(
            CASE WHEN TG_OP = 'INSERT' THEN jsonb_build_array(to_jsonb(NEW))
                 WHEN TG_OP = 'DELETE' THEN jsonb_build_array(to_jsonb(OLD))
                 ELSE jsonb_build_array(to_jsonb(OLD), to_jsonb(NEW)) END)
    LOOP
        IF row_data->>'message_type' IN ('pax:invocation', 'pax:invocation_pending', 'pax:artifact') THEN
            PERFORM pax_search_mark_dirty('message', row_data->>'parent_message_id');
            IF jsonb_typeof(row_data #> '{raw_json,replaces_message_ids}') = 'array' THEN
                FOR replacement_id IN SELECT jsonb_array_elements_text(row_data #> '{raw_json,replaces_message_ids}')
                LOOP
                    PERFORM pax_search_mark_dirty('message', replacement_id);
                END LOOP;
            END IF;
        END IF;
    END LOOP;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS search_message_dirty ON messages;
CREATE TRIGGER search_message_dirty AFTER INSERT OR UPDATE OR DELETE ON messages
FOR EACH ROW EXECUTE FUNCTION pax_search_message_trigger();

CREATE OR REPLACE FUNCTION pax_search_part_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN PERFORM pax_search_mark_dirty('message', OLD.message_id); END IF;
    IF TG_OP <> 'DELETE' AND (TG_OP = 'INSERT' OR NEW.message_id IS DISTINCT FROM OLD.message_id) THEN
        PERFORM pax_search_mark_dirty('message', NEW.message_id);
    END IF;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS search_part_dirty ON message_parts;
CREATE TRIGGER search_part_dirty AFTER INSERT OR UPDATE OR DELETE ON message_parts
FOR EACH ROW EXECUTE FUNCTION pax_search_part_trigger();

CREATE OR REPLACE FUNCTION pax_search_session_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    source_row RECORD;
BEGIN
    IF TG_OP = 'UPDATE' AND
        (NEW.session_name, NEW.custom_session_name, NEW.archived_at, NEW.agent_id, NEW.session_id, NEW.node_id, NEW.conversation_id, NEW.source)
        IS NOT DISTINCT FROM
        (OLD.session_name, OLD.custom_session_name, OLD.archived_at, OLD.agent_id, OLD.session_id, OLD.node_id, OLD.conversation_id, OLD.source)
    THEN RETURN NULL; END IF;
    IF TG_OP <> 'INSERT' THEN PERFORM pax_search_mark_dirty('session', OLD.id::text); END IF;
    IF TG_OP <> 'DELETE' AND (TG_OP = 'INSERT' OR NEW.id IS DISTINCT FROM OLD.id) THEN
        PERFORM pax_search_mark_dirty('session', NEW.id::text);
    END IF;
    IF TG_OP <> 'UPDATE' OR
        (NEW.archived_at, NEW.agent_id, NEW.session_id, NEW.node_id, NEW.source) IS DISTINCT FROM
        (OLD.archived_at, OLD.agent_id, OLD.session_id, OLD.node_id, OLD.source)
    THEN
        FOR source_row IN SELECT message_id FROM messages m WHERE
            (TG_OP <> 'INSERT' AND m.agent_id = OLD.agent_id AND m.session_id = OLD.session_id) OR
            (TG_OP <> 'DELETE' AND m.agent_id = NEW.agent_id AND m.session_id = NEW.session_id)
        LOOP
            PERFORM pax_search_mark_dirty('message', source_row.message_id);
        END LOOP;
    END IF;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS search_session_dirty ON agent_sessions;
CREATE TRIGGER search_session_dirty AFTER INSERT OR UPDATE OR DELETE ON agent_sessions
FOR EACH ROW EXECUTE FUNCTION pax_search_session_trigger();

CREATE OR REPLACE FUNCTION pax_search_agent_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    source_row RECORD;
BEGIN
    IF (NEW.owner_user_id, NEW.deleted_at, NEW.node_id) IS NOT DISTINCT FROM
       (OLD.owner_user_id, OLD.deleted_at, OLD.node_id) THEN RETURN NULL; END IF;
    FOR source_row IN SELECT message_id FROM messages WHERE agent_id = NEW.agent_id LOOP
        PERFORM pax_search_mark_dirty('message', source_row.message_id);
    END LOOP;
    FOR source_row IN SELECT id FROM agent_sessions WHERE agent_id = NEW.agent_id LOOP
        PERFORM pax_search_mark_dirty('session', source_row.id::text);
    END LOOP;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS search_agent_dirty ON agents;
CREATE TRIGGER search_agent_dirty AFTER UPDATE ON agents
FOR EACH ROW EXECUTE FUNCTION pax_search_agent_trigger();

CREATE OR REPLACE FUNCTION pax_search_node_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    source_row RECORD;
BEGIN
    IF NEW.deleted_at IS NOT DISTINCT FROM OLD.deleted_at THEN RETURN NULL; END IF;
    FOR source_row IN SELECT m.message_id FROM messages m
        JOIN agents a ON a.agent_id = m.agent_id
        LEFT JOIN agent_sessions s ON s.agent_id = m.agent_id AND s.session_id = m.session_id
        WHERE NEW.node_id IN (m.node_id, a.node_id, s.node_id)
    LOOP
        PERFORM pax_search_mark_dirty('message', source_row.message_id);
    END LOOP;
    FOR source_row IN SELECT s.id FROM agent_sessions s JOIN agents a ON a.agent_id = s.agent_id
        WHERE NEW.node_id IN (a.node_id, s.node_id)
    LOOP
        PERFORM pax_search_mark_dirty('session', source_row.id::text);
    END LOOP;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS search_node_dirty ON nodes;
CREATE TRIGGER search_node_dirty AFTER UPDATE ON nodes
FOR EACH ROW EXECUTE FUNCTION pax_search_node_trigger();

CREATE OR REPLACE FUNCTION pax_search_summary_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pax_search_mark_dirty('summary', NEW.summary_id);
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS search_summary_dirty ON summary_versions;
CREATE TRIGGER search_summary_dirty AFTER INSERT ON summary_versions
FOR EACH ROW EXECUTE FUNCTION pax_search_summary_trigger();

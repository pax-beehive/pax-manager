CREATE TABLE IF NOT EXISTS secretary_settings (
    owner_user_id TEXT PRIMARY KEY REFERENCES users(user_id),
    agent_id TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    revision BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS secretary_jobs (
    source_id BIGINT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    source_agent_id TEXT NOT NULL,
    source_session_id TEXT NOT NULL,
    desired_revision BIGINT NOT NULL DEFAULT 1,
    processed_revision BIGINT NOT NULL DEFAULT 0,
    retry_generation BIGINT NOT NULL DEFAULT 0,
    reset_revision BIGINT NOT NULL DEFAULT 0,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS secretary_jobs_pending ON secretary_jobs(available_at)
    WHERE desired_revision > processed_revision;
CREATE INDEX IF NOT EXISTS secretary_jobs_owner ON secretary_jobs(owner_user_id);
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS retry_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS reset_revision BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS secretary_message_state (
    source_id BIGINT NOT NULL REFERENCES secretary_jobs(source_id) ON DELETE CASCADE,
    message_id TEXT NOT NULL,
    change_revision BIGINT NOT NULL,
    processed_hash TEXT NOT NULL DEFAULT '',
    processed_owner_user_id TEXT NOT NULL DEFAULT '',
    processed_canonicalization_version TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(source_id, message_id)
);
CREATE INDEX IF NOT EXISTS secretary_message_changes ON secretary_message_state(source_id, change_revision);
ALTER TABLE secretary_message_state ADD COLUMN IF NOT EXISTS processed_owner_user_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS secretary_internal_sessions (
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(agent_id, session_id)
);

CREATE OR REPLACE FUNCTION pax_secretary_mark_dirty(source_agent TEXT, source_session TEXT, source_message TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    source_row RECORD;
    next_revision BIGINT;
BEGIN
    SELECT s.id, a.owner_user_id INTO source_row FROM agent_sessions s
        JOIN agents a ON a.agent_id = s.agent_id
        JOIN secretary_settings cfg ON cfg.owner_user_id = a.owner_user_id AND cfg.enabled
        WHERE s.agent_id = source_agent AND s.session_id = source_session
        AND s.archived_at IS NULL AND a.deleted_at IS NULL
        AND COALESCE(s.source,'') <> 'secretary_internal'
        AND NOT EXISTS (SELECT 1 FROM secretary_internal_sessions i
            WHERE i.agent_id = s.agent_id AND i.session_id = s.session_id);
    IF NOT FOUND THEN RETURN; END IF;
    INSERT INTO secretary_jobs(source_id, owner_user_id, source_agent_id, source_session_id)
    VALUES (source_row.id, source_row.owner_user_id, source_agent, source_session)
    ON CONFLICT(source_id) DO UPDATE SET desired_revision = secretary_jobs.desired_revision + 1,
        lease_token = CASE WHEN secretary_jobs.owner_user_id <> EXCLUDED.owner_user_id THEN '' ELSE secretary_jobs.lease_token END,
        lease_until = CASE WHEN secretary_jobs.owner_user_id <> EXCLUDED.owner_user_id THEN NULL ELSE secretary_jobs.lease_until END,
        owner_user_id = EXCLUDED.owner_user_id, updated_at = NOW(), available_at = NOW()
    RETURNING desired_revision INTO next_revision;
    INSERT INTO secretary_message_state(source_id, message_id, change_revision)
    VALUES (source_row.id, source_message, next_revision)
    ON CONFLICT(source_id, message_id) DO UPDATE SET change_revision = EXCLUDED.change_revision;
END $$;

CREATE OR REPLACE FUNCTION pax_secretary_message_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM pax_secretary_mark_dirty(OLD.agent_id, OLD.session_id, OLD.message_id);
    END IF;
    IF TG_OP <> 'DELETE' AND (TG_OP = 'INSERT' OR
        (NEW.agent_id, NEW.session_id, NEW.message_id) IS DISTINCT FROM
        (OLD.agent_id, OLD.session_id, OLD.message_id)) THEN
        PERFORM pax_secretary_mark_dirty(NEW.agent_id, NEW.session_id, NEW.message_id);
    END IF;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS secretary_message_dirty ON messages;
CREATE TRIGGER secretary_message_dirty AFTER INSERT OR UPDATE OR DELETE ON messages
FOR EACH ROW EXECUTE FUNCTION pax_secretary_message_trigger();

CREATE OR REPLACE FUNCTION pax_secretary_part_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    message_row RECORD;
BEGIN
    FOR message_row IN SELECT m.agent_id, m.session_id, m.message_id FROM messages m WHERE
        (TG_OP <> 'INSERT' AND m.message_id = OLD.message_id) OR
        (TG_OP <> 'DELETE' AND m.message_id = NEW.message_id)
    LOOP
        PERFORM pax_secretary_mark_dirty(message_row.agent_id, message_row.session_id, message_row.message_id);
    END LOOP;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS secretary_part_dirty ON message_parts;
CREATE TRIGGER secretary_part_dirty AFTER INSERT OR UPDATE OR DELETE ON message_parts
FOR EACH ROW EXECUTE FUNCTION pax_secretary_part_trigger();

CREATE OR REPLACE FUNCTION pax_secretary_agent_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    source_message RECORD;
BEGIN
    IF NEW.owner_user_id IS NOT DISTINCT FROM OLD.owner_user_id THEN RETURN NULL; END IF;
    FOR source_message IN SELECT message_id,session_id FROM messages WHERE agent_id=NEW.agent_id
    LOOP
        PERFORM pax_secretary_mark_dirty(NEW.agent_id,source_message.session_id,source_message.message_id);
    END LOOP;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS secretary_agent_dirty ON agents;
CREATE TRIGGER secretary_agent_dirty AFTER UPDATE ON agents
FOR EACH ROW EXECUTE FUNCTION pax_secretary_agent_trigger();

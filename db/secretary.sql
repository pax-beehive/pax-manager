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
    auto_retry_key TEXT NOT NULL DEFAULT '',
    auto_retry_count INTEGER NOT NULL DEFAULT 0,
    last_validation_failure TEXT NOT NULL DEFAULT '',
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
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS auto_retry_key TEXT NOT NULL DEFAULT '';
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS auto_retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS last_validation_failure TEXT NOT NULL DEFAULT '';
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS reset_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS retry_epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE secretary_jobs ADD COLUMN IF NOT EXISTS execution_blocked BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS secretary_batches (
    batch_id TEXT PRIMARY KEY,
    source_id BIGINT NOT NULL,
    owner_user_id TEXT NOT NULL,
    input_json JSONB NOT NULL,
    settings_revision BIGINT NOT NULL,
    retry_epoch BIGINT NOT NULL DEFAULT 0,
    through_revision BIGINT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('pending','running','output_received','committed','blocked','unknown','superseded')),
    lease_token TEXT NOT NULL DEFAULT '',
    current_attempt_id TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    request_hash TEXT NOT NULL DEFAULT '',
    results_json JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK(octet_length(input_json::text) <= 16*1024*1024)
);
CREATE INDEX IF NOT EXISTS secretary_batches_source ON secretary_batches(source_id,updated_at DESC);
CREATE INDEX IF NOT EXISTS secretary_batches_open ON secretary_batches(source_id,owner_user_id,settings_revision,retry_epoch)
    WHERE state IN ('pending','running','output_received','unknown');
CREATE INDEX IF NOT EXISTS secretary_batches_owner_unfinished ON secretary_batches(owner_user_id,settings_revision,updated_at DESC,batch_id DESC)
    WHERE state IN ('pending','running','output_received','unknown','blocked');

CREATE TABLE IF NOT EXISTS secretary_attempts (
    attempt_id TEXT PRIMARY KEY,
    batch_id TEXT NOT NULL REFERENCES secretary_batches(batch_id),
    attempt_number INTEGER NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('running','output_received','invalid','failed','unknown','canceled','superseded','committed')),
    output_text TEXT NOT NULL DEFAULT '',
    reads_json JSONB NOT NULL DEFAULT '{}',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(batch_id,attempt_number)
);

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
ALTER TABLE secretary_message_state ADD COLUMN IF NOT EXISTS last_batch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE secretary_message_state ADD COLUMN IF NOT EXISTS processing_result JSONB NOT NULL DEFAULT '{}';

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

CREATE OR REPLACE FUNCTION pax_secretary_wake_visible_message(target_message TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE source_row RECORD;
BEGIN
    SELECT agent_id,session_id,role,message_type INTO source_row FROM messages WHERE message_id=target_message;
    IF NOT FOUND OR source_row.role NOT IN ('user','assistant') OR COALESCE(source_row.message_type,'') NOT IN
      ('','chat','text','assistant_message','agent_message_chunk','message_delta','user_message',
       'pax:user_message','pax:invocation','pax:invocation_pending','pax:artifact') THEN RETURN; END IF;
    UPDATE secretary_jobs SET execution_blocked=FALSE WHERE source_agent_id=source_row.agent_id
      AND source_session_id=source_row.session_id AND execution_blocked;
    PERFORM pax_knowledge_invalidate_source(target_message);
END $$;

CREATE OR REPLACE FUNCTION pax_secretary_visible_part_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        IF OLD.part_type='text' THEN PERFORM pax_secretary_wake_visible_message(OLD.message_id); END IF;
    ELSIF TG_OP='INSERT' THEN
        IF NEW.part_type='text' THEN PERFORM pax_secretary_wake_visible_message(NEW.message_id); END IF;
    ELSIF (NEW.message_id,NEW.part_index,NEW.part_type,NEW.text) IS DISTINCT FROM
          (OLD.message_id,OLD.part_index,OLD.part_type,OLD.text) THEN
        IF OLD.part_type='text' THEN PERFORM pax_secretary_wake_visible_message(OLD.message_id); END IF;
        IF NEW.part_type='text' THEN PERFORM pax_secretary_wake_visible_message(NEW.message_id); END IF;
    END IF;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS secretary_visible_part ON message_parts;
CREATE TRIGGER secretary_visible_part AFTER INSERT OR UPDATE OR DELETE ON message_parts
FOR EACH ROW EXECUTE FUNCTION pax_secretary_visible_part_trigger();

CREATE OR REPLACE FUNCTION pax_secretary_visible_message_trigger()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        IF OLD.role IN ('user','assistant') AND COALESCE(OLD.message_type,'') IN
          ('','chat','text','assistant_message','agent_message_chunk','message_delta','user_message',
           'pax:user_message','pax:invocation','pax:invocation_pending','pax:artifact') THEN
            UPDATE secretary_jobs SET execution_blocked=FALSE WHERE source_agent_id=OLD.agent_id AND source_session_id=OLD.session_id;
            PERFORM pax_knowledge_invalidate_source(OLD.message_id);
        END IF;
    ELSIF TG_OP='INSERT' THEN
        PERFORM pax_secretary_wake_visible_message(NEW.message_id);
    ELSIF (NEW.agent_id,NEW.session_id,NEW.role,NEW.message_type,NEW.raw_json) IS DISTINCT FROM
          (OLD.agent_id,OLD.session_id,OLD.role,OLD.message_type,OLD.raw_json) THEN
        PERFORM pax_secretary_wake_visible_message(NEW.message_id);
        PERFORM pax_knowledge_invalidate_source(NEW.message_id);
    END IF;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS secretary_visible_message ON messages;
CREATE TRIGGER secretary_visible_message AFTER INSERT OR UPDATE OR DELETE ON messages
FOR EACH ROW EXECUTE FUNCTION pax_secretary_visible_message_trigger();

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
CREATE OR REPLACE FUNCTION pax_knowledge_invalidate_scope(changed_agent TEXT, changed_session TEXT DEFAULT NULL)
RETURNS VOID AS $$
DECLARE source RECORD;
BEGIN
    FOR source IN SELECT DISTINCT snapshot.message_id FROM message_snapshots snapshot
        WHERE snapshot.agent_id=changed_agent AND (changed_session IS NULL OR snapshot.session_id=changed_session)
        AND EXISTS(SELECT 1 FROM summary_sources ref WHERE ref.message_id=snapshot.message_id)
    LOOP
        PERFORM pax_knowledge_invalidate_source(source.message_id);
    END LOOP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION pax_knowledge_agent_access_changed() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP='DELETE' OR NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id
        OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
        PERFORM pax_knowledge_invalidate_scope(OLD.agent_id);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS knowledge_agent_access_changed ON agents;
CREATE TRIGGER knowledge_agent_access_changed AFTER UPDATE OF owner_user_id,deleted_at OR DELETE ON agents
    FOR EACH ROW EXECUTE FUNCTION pax_knowledge_agent_access_changed();

CREATE OR REPLACE FUNCTION pax_knowledge_session_access_changed() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP='DELETE' OR NEW.archived_at IS DISTINCT FROM OLD.archived_at
        OR NEW.node_id IS DISTINCT FROM OLD.node_id THEN
        PERFORM pax_knowledge_invalidate_scope(OLD.agent_id,OLD.session_id);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS knowledge_session_access_changed ON agent_sessions;
CREATE TRIGGER knowledge_session_access_changed AFTER UPDATE OF archived_at,node_id OR DELETE ON agent_sessions
    FOR EACH ROW EXECUTE FUNCTION pax_knowledge_session_access_changed();

CREATE OR REPLACE FUNCTION pax_knowledge_node_access_changed() RETURNS TRIGGER AS $$
DECLARE source RECORD;
BEGIN
    IF TG_OP='DELETE' OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
        FOR source IN SELECT agent_id FROM agents WHERE node_id=OLD.node_id LOOP
            PERFORM pax_knowledge_invalidate_scope(source.agent_id);
        END LOOP;
        FOR source IN SELECT agent_id,session_id FROM agent_sessions WHERE node_id=OLD.node_id LOOP
            PERFORM pax_knowledge_invalidate_scope(source.agent_id,source.session_id);
        END LOOP;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS knowledge_node_access_changed ON nodes;
CREATE TRIGGER knowledge_node_access_changed BEFORE UPDATE OF deleted_at OR DELETE ON nodes
    FOR EACH ROW EXECUTE FUNCTION pax_knowledge_node_access_changed();

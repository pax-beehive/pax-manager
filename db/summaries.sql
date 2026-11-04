CREATE TABLE IF NOT EXISTS summary_versions (
    summary_id TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    owner_user_id TEXT NOT NULL,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('fragment', 'topic', 'direction', 'project')),
    subject_id TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    claims_json JSONB NOT NULL DEFAULT '[]',
    request_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (summary_id, revision),
    UNIQUE (owner_user_id, request_id)
);

CREATE INDEX IF NOT EXISTS summary_versions_subject_idx
    ON summary_versions (owner_user_id, subject_type, subject_id, summary_id, revision DESC);

CREATE INDEX IF NOT EXISTS summary_versions_owner_browse_idx
    ON summary_versions (owner_user_id, summary_id, revision DESC);

CREATE TABLE IF NOT EXISTS knowledge_nodes (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type = 'summary'),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','empty','stale')),
    current_revision BIGINT NOT NULL CHECK (current_revision > 0),
    origin_source_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE knowledge_nodes ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','empty','stale'));

CREATE INDEX IF NOT EXISTS knowledge_nodes_origin_idx
    ON knowledge_nodes (owner_user_id, origin_source_id, id);

CREATE TABLE IF NOT EXISTS knowledge_node_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS knowledge_node_invalidations (
    node_id TEXT NOT NULL REFERENCES knowledge_nodes(id),
    message_id TEXT NOT NULL,
    target_revision BIGINT NOT NULL,
    origin_source_id TEXT NOT NULL,
    change_revision BIGINT NOT NULL,
    PRIMARY KEY (node_id,message_id)
);

CREATE INDEX IF NOT EXISTS knowledge_node_invalidations_origin_idx
    ON knowledge_node_invalidations(origin_source_id,change_revision);

DO $$ BEGIN
IF NOT EXISTS(SELECT 1 FROM knowledge_node_migrations WHERE version='identity-v1') THEN
INSERT INTO knowledge_nodes(id,owner_user_id,type,status,current_revision,origin_source_id,created_at,updated_at)
SELECT head.summary_id,head.owner_user_id,'summary',
    CASE WHEN head.body='No supported content remains in this summary.' AND head.title='Unavailable summary' THEN 'empty' ELSE 'active' END,
    head.revision,
    CASE WHEN head.subject_type='fragment' AND starts_with(head.subject_id,'secretary:session:')
        THEN substring(head.subject_id FROM length('secretary:session:')+1) ELSE '' END,
    first.created_at,head.created_at
FROM (SELECT DISTINCT ON (summary_id) * FROM summary_versions ORDER BY summary_id,revision DESC) head
JOIN summary_versions first ON first.summary_id=head.summary_id AND first.revision=1
ON CONFLICT (id) DO NOTHING;
INSERT INTO knowledge_node_migrations(version) VALUES('identity-v1') ON CONFLICT DO NOTHING;
END IF;
END $$;

CREATE OR REPLACE FUNCTION pax_summary_update_node() RETURNS TRIGGER AS $$
DECLARE origin TEXT;
BEGIN
    origin := CASE WHEN NEW.subject_type='fragment' AND starts_with(NEW.subject_id,'secretary:session:')
        THEN substring(NEW.subject_id FROM length('secretary:session:')+1) ELSE '' END;
    INSERT INTO knowledge_nodes(id,owner_user_id,type,status,current_revision,origin_source_id,created_at,updated_at)
    VALUES(NEW.summary_id,NEW.owner_user_id,'summary',
        CASE WHEN NEW.body='No supported content remains in this summary.' AND NEW.title='Unavailable summary' THEN 'empty' ELSE 'active' END,
        NEW.revision,origin,NEW.created_at,NEW.created_at)
    ON CONFLICT (id) DO UPDATE SET current_revision=EXCLUDED.current_revision,updated_at=EXCLUDED.updated_at,status=EXCLUDED.status
    WHERE knowledge_nodes.owner_user_id=EXCLUDED.owner_user_id
        AND knowledge_nodes.type='summary' AND knowledge_nodes.origin_source_id=EXCLUDED.origin_source_id
        AND knowledge_nodes.current_revision<EXCLUDED.current_revision;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'summary node owner, origin, or revision conflict';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS summary_versions_node ON summary_versions;
CREATE TRIGGER summary_versions_node AFTER INSERT ON summary_versions
    FOR EACH ROW EXECUTE FUNCTION pax_summary_update_node();

CREATE TABLE IF NOT EXISTS message_snapshots (
    message_id TEXT NOT NULL,
    canonicalization_version TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    session_seq BIGINT NOT NULL DEFAULT 0,
    message_created_at TIMESTAMPTZ NOT NULL,
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (message_id, canonicalization_version, content_hash)
);

CREATE INDEX IF NOT EXISTS message_snapshots_session_idx
    ON message_snapshots(agent_id,session_id,message_id);

CREATE TABLE IF NOT EXISTS summary_sources (
    summary_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    source_order INTEGER NOT NULL CHECK (source_order >= 0),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('message', 'summary')),
    message_id TEXT,
    canonicalization_version TEXT,
    content_hash TEXT,
    child_summary_id TEXT,
    child_revision BIGINT,
    PRIMARY KEY (summary_id, revision, source_order),
    FOREIGN KEY (summary_id, revision) REFERENCES summary_versions (summary_id, revision) ON DELETE CASCADE,
    FOREIGN KEY (message_id, canonicalization_version, content_hash)
        REFERENCES message_snapshots (message_id, canonicalization_version, content_hash),
    FOREIGN KEY (child_summary_id, child_revision) REFERENCES summary_versions (summary_id, revision),
    CHECK ((source_kind = 'message' AND message_id IS NOT NULL AND canonicalization_version IS NOT NULL
        AND content_hash IS NOT NULL AND child_summary_id IS NULL AND child_revision IS NULL)
        OR (source_kind = 'summary' AND message_id IS NULL AND canonicalization_version IS NULL
        AND content_hash IS NULL AND child_summary_id IS NOT NULL AND child_revision IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS summary_sources_message_idx ON summary_sources (message_id)
    WHERE source_kind = 'message';
CREATE INDEX IF NOT EXISTS summary_sources_child_idx ON summary_sources (child_summary_id, child_revision)
    WHERE source_kind = 'summary';

CREATE TABLE IF NOT EXISTS knowledge_node_sources (
    node_id TEXT NOT NULL REFERENCES knowledge_nodes(id),
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    PRIMARY KEY (node_id,agent_id,session_id)
);

DO $$ BEGIN
IF NOT EXISTS(SELECT 1 FROM knowledge_node_migrations WHERE version='sources-v1') THEN
WITH RECURSIVE dependencies(node_id,summary_id,revision) AS (
    SELECT summary_id,summary_id,revision FROM summary_versions
    UNION
    SELECT d.node_id,src.child_summary_id,src.child_revision FROM dependencies d
    JOIN summary_sources src USING(summary_id,revision) WHERE src.source_kind='summary'
)
INSERT INTO knowledge_node_sources(node_id,agent_id,session_id)
SELECT DISTINCT d.node_id,snapshot.agent_id,snapshot.session_id FROM dependencies d
JOIN summary_sources src USING(summary_id,revision)
JOIN message_snapshots snapshot USING(message_id,canonicalization_version,content_hash)
WHERE src.source_kind='message' ON CONFLICT DO NOTHING;
INSERT INTO knowledge_node_migrations(version) VALUES('sources-v1') ON CONFLICT DO NOTHING;
END IF;
END $$;

CREATE OR REPLACE FUNCTION pax_summary_update_source_directory() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.source_kind='message' THEN
        INSERT INTO knowledge_node_sources(node_id,agent_id,session_id)
        SELECT NEW.summary_id,agent_id,session_id FROM message_snapshots
        WHERE message_id=NEW.message_id AND canonicalization_version=NEW.canonicalization_version
            AND content_hash=NEW.content_hash ON CONFLICT DO NOTHING;
    ELSE
        WITH RECURSIVE dependencies(summary_id,revision) AS (
            SELECT NEW.child_summary_id,NEW.child_revision
            UNION
            SELECT src.child_summary_id,src.child_revision FROM dependencies d
            JOIN summary_sources src USING(summary_id,revision) WHERE src.source_kind='summary'
        )
        INSERT INTO knowledge_node_sources(node_id,agent_id,session_id)
        SELECT DISTINCT NEW.summary_id,snapshot.agent_id,snapshot.session_id FROM dependencies d
        JOIN summary_sources src USING(summary_id,revision)
        JOIN message_snapshots snapshot USING(message_id,canonicalization_version,content_hash)
        WHERE src.source_kind='message' ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS summary_sources_directory ON summary_sources;
CREATE TRIGGER summary_sources_directory AFTER INSERT ON summary_sources
    FOR EACH ROW EXECUTE FUNCTION pax_summary_update_source_directory();

CREATE OR REPLACE FUNCTION pax_knowledge_invalidate_source(changed_message_id TEXT) RETURNS VOID AS $$
DECLARE target RECORD;
DECLARE changed_revision BIGINT;
BEGIN
    FOR target IN
        WITH RECURSIVE affected(summary_id,revision) AS (
            SELECT summary_id,revision FROM summary_sources WHERE message_id=changed_message_id
            UNION
            SELECT parent.summary_id,parent.revision FROM summary_sources parent
            JOIN affected child ON parent.child_summary_id=child.summary_id AND parent.child_revision=child.revision
        )
        SELECT DISTINCT node.origin_source_id,node.owner_user_id FROM affected
        JOIN knowledge_nodes node ON node.id=affected.summary_id AND node.current_revision=affected.revision
        WHERE node.origin_source_id<>''
    LOOP
        UPDATE secretary_jobs SET desired_revision=desired_revision+1,updated_at=NOW(),available_at=NOW(),execution_blocked=FALSE
        WHERE source_id::text=target.origin_source_id AND owner_user_id=target.owner_user_id
        RETURNING desired_revision INTO changed_revision;
            INSERT INTO knowledge_node_invalidations(node_id,message_id,target_revision,origin_source_id,change_revision)
            WITH RECURSIVE affected(summary_id,revision) AS (
                SELECT summary_id,revision FROM summary_sources WHERE message_id=changed_message_id
                UNION SELECT parent.summary_id,parent.revision FROM summary_sources parent
                JOIN affected child ON parent.child_summary_id=child.summary_id AND parent.child_revision=child.revision
            )
            SELECT DISTINCT node.id,changed_message_id,node.current_revision,node.origin_source_id,COALESCE(changed_revision,0)
            FROM affected JOIN knowledge_nodes node ON node.id=affected.summary_id AND node.current_revision=affected.revision
            WHERE node.origin_source_id=target.origin_source_id AND node.owner_user_id=target.owner_user_id
            ON CONFLICT(node_id,message_id) DO UPDATE SET target_revision=EXCLUDED.target_revision,
                change_revision=EXCLUDED.change_revision;
            UPDATE knowledge_nodes SET status='stale' WHERE origin_source_id=target.origin_source_id
                AND owner_user_id=target.owner_user_id AND EXISTS(
                    SELECT 1 FROM knowledge_node_invalidations invalidation WHERE invalidation.node_id=knowledge_nodes.id
                    AND invalidation.target_revision=knowledge_nodes.current_revision);
    END LOOP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION pax_summary_reject_update() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'summary versions and source snapshots are immutable';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS summary_versions_immutable ON summary_versions;
CREATE TRIGGER summary_versions_immutable BEFORE UPDATE ON summary_versions
    FOR EACH ROW EXECUTE FUNCTION pax_summary_reject_update();
DROP TRIGGER IF EXISTS message_snapshots_immutable ON message_snapshots;
CREATE TRIGGER message_snapshots_immutable BEFORE UPDATE ON message_snapshots
    FOR EACH ROW EXECUTE FUNCTION pax_summary_reject_update();
DROP TRIGGER IF EXISTS summary_sources_immutable ON summary_sources;
CREATE TRIGGER summary_sources_immutable BEFORE UPDATE ON summary_sources
    FOR EACH ROW EXECUTE FUNCTION pax_summary_reject_update();

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

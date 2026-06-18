CREATE OR REPLACE FUNCTION computed_status(last_seen TIMESTAMPTZ)
RETURNS TEXT AS $$
BEGIN
    IF last_seen IS NULL THEN
        RETURN 'offline';
    END IF;
    IF last_seen >= NOW() - INTERVAL '30 seconds' THEN
        RETURN 'online';
    END IF;
    IF last_seen >= NOW() - INTERVAL '5 minutes' THEN
        RETURN 'degraded';
    END IF;
    RETURN 'offline';
END;
$$ LANGUAGE plpgsql STABLE;

CREATE TABLE IF NOT EXISTS users (
    user_id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS agents (
    agent_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    name TEXT NOT NULL DEFAULT '',
    hostname TEXT NOT NULL,
    agent_type TEXT NOT NULL DEFAULT 'hermes',
    machine_type TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL,
    hermes_version TEXT NOT NULL DEFAULT '',
    api_endpoint TEXT NOT NULL DEFAULT 'http://localhost:8642',
    api_key_hash TEXT UNIQUE NOT NULL,
    status TEXT NOT NULL DEFAULT 'offline',
    last_heartbeat TIMESTAMPTZ,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB
);

CREATE TABLE IF NOT EXISTS nodes (
    node_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    name TEXT NOT NULL DEFAULT '',
    hostname TEXT NOT NULL,
    machine_type TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT 'unknown',
    arch TEXT NOT NULL DEFAULT '',
    paxd_version TEXT NOT NULL DEFAULT '',
    api_endpoint TEXT NOT NULL DEFAULT 'http://localhost:8642',
    api_key_hash TEXT UNIQUE NOT NULL,
    status TEXT NOT NULL DEFAULT 'offline',
    last_heartbeat TIMESTAMPTZ,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB
);

CREATE INDEX IF NOT EXISTS idx_nodes_owner ON nodes(owner_user_id);

ALTER TABLE agents ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE agents ADD COLUMN IF NOT EXISTS node_id TEXT REFERENCES nodes(node_id);
ALTER TABLE agents ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS agent_type TEXT NOT NULL DEFAULT 'hermes';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS machine_type TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS os TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS hermes_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS api_endpoint TEXT NOT NULL DEFAULT 'http://localhost:8642';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS metadata JSONB;

CREATE INDEX IF NOT EXISTS idx_agents_owner ON agents(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_agents_node ON agents(node_id);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id BIGSERIAL PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    session_name TEXT,
    agent_type TEXT,
    native_id TEXT,
    project_id TEXT,
    preview TEXT,
    workspace_roots JSONB NOT NULL DEFAULT '[]'::jsonb,
    source TEXT,
    status TEXT NOT NULL DEFAULT 'idle',
    current_task TEXT,
    last_message_at TIMESTAMPTZ,
    message_count INTEGER NOT NULL DEFAULT 0,
    token_input BIGINT NOT NULL DEFAULT 0,
    token_output BIGINT NOT NULL DEFAULT 0,
    token_total BIGINT NOT NULL DEFAULT 0,
    model TEXT,
    run_id TEXT,
    run_status TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, session_id)
);

ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS session_name TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS node_id TEXT REFERENCES nodes(node_id);
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS agent_type TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS native_id TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS project_id TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS preview TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS workspace_roots JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS source TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS current_task TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS last_message_at TIMESTAMPTZ;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS message_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS token_input BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS token_output BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS token_total BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS cache_read_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS cache_write_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS cache_creation_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS reasoning_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS estimated_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS actual_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS metadata JSONB;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS model TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS run_id TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS run_status TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_sessions_agent ON agent_sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_agent_native ON agent_sessions(agent_id, native_id)
    WHERE native_id IS NOT NULL AND native_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_status ON agent_sessions(status);

CREATE TABLE IF NOT EXISTS mailbox (
    id BIGSERIAL PRIMARY KEY,
    message_id TEXT UNIQUE NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(user_id),
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT,
    message TEXT NOT NULL,
    message_type TEXT NOT NULL DEFAULT 'chat',
    payload JSONB,
    status TEXT NOT NULL DEFAULT 'pending',
    delivered_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    result TEXT,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ
);

ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS node_id TEXT REFERENCES nodes(node_id);
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS payload JSONB;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS result TEXT;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS error TEXT;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT 'user_to_node';
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS parent_message_id TEXT;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS turn_id TEXT;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS response_id TEXT;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS events JSONB;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS file_changes JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE mailbox ADD COLUMN IF NOT EXISTS token_usage JSONB;

CREATE INDEX IF NOT EXISTS idx_mailbox_owner ON mailbox(owner_user_id, id);
CREATE INDEX IF NOT EXISTS idx_mailbox_agent ON mailbox(agent_id, status, id);
CREATE INDEX IF NOT EXISTS idx_mailbox_node ON mailbox(node_id, status, id);
CREATE INDEX IF NOT EXISTS idx_mailbox_created ON mailbox(created_at);
CREATE INDEX IF NOT EXISTS idx_mailbox_session ON mailbox(session_id, id);

CREATE TABLE IF NOT EXISTS message_offsets (
    agent_id TEXT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    last_offset BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS node_message_offsets (
    node_id TEXT PRIMARY KEY REFERENCES nodes(node_id) ON DELETE CASCADE,
    last_offset BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS agent_registration_tokens (
    token_hash TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    expires_at TIMESTAMPTZ,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_registration_tokens_owner ON agent_registration_tokens(owner_user_id);

CREATE TABLE IF NOT EXISTS user_api_keys (
    key_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    key_hash TEXT UNIQUE NOT NULL,
    prefix TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_user_api_keys_owner ON user_api_keys(owner_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_user_api_keys_hash ON user_api_keys(key_hash);

CREATE TABLE IF NOT EXISTS secrets (
    secret_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    current_version_id TEXT,
    current_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_secrets_owner ON secrets(owner_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS secret_versions (
    version_id TEXT PRIMARY KEY,
    secret_id TEXT NOT NULL REFERENCES secrets(secret_id) ON DELETE CASCADE,
    version_number BIGINT NOT NULL,
    ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    key_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by_user_id TEXT REFERENCES users(user_id),
    created_by_node_id TEXT REFERENCES nodes(node_id),
    created_by_agent_id TEXT REFERENCES agents(agent_id),
    idempotency_key TEXT NOT NULL DEFAULT '',
    UNIQUE(secret_id, version_number)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_versions_idempotency
    ON secret_versions(secret_id, created_by_node_id, created_by_agent_id, idempotency_key)
    WHERE idempotency_key <> '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'secrets_current_version_fk'
    ) THEN
        ALTER TABLE secrets ADD CONSTRAINT secrets_current_version_fk
            FOREIGN KEY (current_version_id)
            REFERENCES secret_versions(version_id)
            DEFERRABLE INITIALLY DEFERRED;
    END IF;
END;
$$;

CREATE INDEX IF NOT EXISTS idx_secret_versions_secret_created
    ON secret_versions(secret_id, version_number DESC);

CREATE TABLE IF NOT EXISTS secret_access_events (
    event_id BIGSERIAL PRIMARY KEY,
    secret_id TEXT NOT NULL,
    version_id TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_secret_access_events_secret_created
    ON secret_access_events(secret_id, created_at DESC);

CREATE TABLE IF NOT EXISTS paxd_artifacts (
    artifact_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}'::text[],
    version TEXT NOT NULL,
    build_id TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL,
    object TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE(bucket, object, generation)
);

ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS build_id TEXT NOT NULL DEFAULT '';
ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS size_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS content_type TEXT NOT NULL DEFAULT '';
ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_paxd_artifacts_platform_created
    ON paxd_artifacts(platform, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_paxd_artifacts_tags
    ON paxd_artifacts USING GIN(tags)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_approvals (
    approval_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    request_node_id TEXT REFERENCES nodes(node_id),
    request_agent_id TEXT REFERENCES agents(agent_id),
    request_session_id TEXT NOT NULL DEFAULT '',
    source_message_id TEXT NOT NULL DEFAULT '',
    grant_node_id TEXT NOT NULL DEFAULT '',
    grant_agent_id TEXT NOT NULL DEFAULT '',
    grant_session_id TEXT NOT NULL DEFAULT '',
    domain TEXT NOT NULL,
    operation TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_ref TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    risk_level TEXT NOT NULL DEFAULT 'unknown',
    action_fingerprint TEXT NOT NULL,
    request_body JSONB NOT NULL DEFAULT '{}'::jsonb,
    requested_effects JSONB NOT NULL DEFAULT '[]'::jsonb,
    options JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending',
    decision TEXT NOT NULL DEFAULT '',
    decision_option TEXT NOT NULL DEFAULT '',
    decision_scope TEXT NOT NULL DEFAULT '',
    grant_body JSONB NOT NULL DEFAULT '{}'::jsonb,
    decided_by_user_id TEXT REFERENCES users(user_id),
    grant_revoked_at TIMESTAMPTZ,
    grant_revoked_by_user_id TEXT REFERENCES users(user_id),
    grant_revocation_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    decided_at TIMESTAMPTZ,
    raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb
);

ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS request_node_id TEXT REFERENCES nodes(node_id);
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS request_agent_id TEXT REFERENCES agents(agent_id);
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS request_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS source_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS domain TEXT NOT NULL DEFAULT 'agent_action';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS operation TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS resource_type TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS resource_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS risk_level TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS action_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS request_body JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS requested_effects JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS options JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS decision TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS decision_option TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS decision_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_body JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS decided_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_revoked_at TIMESTAMPTZ;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_revoked_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS grant_revocation_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS decided_at TIMESTAMPTZ;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_agent_approvals_owner_status
    ON agent_approvals(owner_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_approvals_request_origin
    ON agent_approvals(owner_user_id, request_node_id, request_agent_id, request_session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_approvals_grant_lookup
    ON agent_approvals(
        owner_user_id,
        domain,
        operation,
        action_fingerprint,
        decision,
        grant_node_id,
        grant_agent_id,
        grant_session_id,
        decided_at DESC
    );
CREATE INDEX IF NOT EXISTS idx_agent_approvals_resource
    ON agent_approvals(owner_user_id, resource_type, resource_ref, created_at DESC);

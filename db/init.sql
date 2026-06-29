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
    description TEXT NOT NULL DEFAULT '',
    card JSONB NOT NULL DEFAULT '{}'::jsonb,
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
    user_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB
);

CREATE TABLE IF NOT EXISTS nodes (
    node_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    kind TEXT NOT NULL DEFAULT 'paxd',
    name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
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
    user_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB
);

CREATE INDEX IF NOT EXISTS idx_nodes_owner ON nodes(owner_user_id);

ALTER TABLE nodes ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'paxd';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS user_metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE agents ADD COLUMN IF NOT EXISTS node_id TEXT REFERENCES nodes(node_id);
ALTER TABLE agents ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS card JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS agent_type TEXT NOT NULL DEFAULT 'hermes';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS machine_type TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS os TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS hermes_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS api_endpoint TEXT NOT NULL DEFAULT 'http://localhost:8642';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS user_metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
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

CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    message_id TEXT UNIQUE NOT NULL,
    owner_user_id TEXT REFERENCES users(user_id),
    node_id TEXT REFERENCES nodes(node_id),
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT,
    source TEXT NOT NULL,
    direction TEXT NOT NULL,
    role TEXT,
    status TEXT,
    message_type TEXT,
    parent_message_id TEXT,
    turn_id TEXT,
    response_id TEXT,
    logical_key TEXT UNIQUE,
    raw_json JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS message_parts (
    id BIGSERIAL PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(message_id) ON DELETE CASCADE,
    part_index INTEGER NOT NULL,
    part_type TEXT NOT NULL,
    text TEXT,
    payload_json JSONB,
    artifact_uri TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(message_id, part_index)
);

CREATE INDEX IF NOT EXISTS idx_messages_agent_created ON messages(agent_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_messages_session_created ON messages(session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_message_parts_message ON message_parts(message_id, part_index);

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

CREATE TABLE IF NOT EXISTS transport_journal (
    id BIGSERIAL PRIMARY KEY,
    queue_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    stream TEXT NOT NULL,
    seq BIGINT NOT NULL,
    direction TEXT NOT NULL,
    local_direction TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'data',
    payload_json JSONB,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    error TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    received_at TIMESTAMPTZ,
    acked_at TIMESTAMPTZ,
    applied_at TIMESTAMPTZ,
    UNIQUE(queue_id, stream, seq, direction)
);

ALTER TABLE transport_journal ADD COLUMN IF NOT EXISTS queue_id TEXT;
UPDATE transport_journal SET queue_id = agent_id WHERE queue_id IS NULL OR queue_id = '';
ALTER TABLE transport_journal ALTER COLUMN queue_id SET NOT NULL;

ALTER TABLE transport_journal ADD COLUMN IF NOT EXISTS direction TEXT;
UPDATE transport_journal SET direction = local_direction WHERE direction IS NULL OR direction = '';
ALTER TABLE transport_journal ALTER COLUMN direction SET NOT NULL;

ALTER TABLE transport_journal ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'data';
ALTER TABLE transport_journal ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE transport_journal ADD COLUMN IF NOT EXISTS error_message TEXT NOT NULL DEFAULT '';
UPDATE transport_journal SET error_message = COALESCE(error, '') WHERE error_message = '';
ALTER TABLE transport_journal ALTER COLUMN payload_json DROP NOT NULL;
UPDATE transport_journal
SET stream = 'acp'
WHERE stream IN ('manager_to_paxd', 'paxd_to_manager');

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'transport_journal_agent_id_stream_seq_local_direction_key'
    ) THEN
        ALTER TABLE transport_journal
            DROP CONSTRAINT transport_journal_agent_id_stream_seq_local_direction_key;
    END IF;
END;
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_transport_journal_queue_unique
    ON transport_journal(queue_id, stream, seq, direction);
DROP INDEX IF EXISTS idx_transport_journal_pending;
CREATE INDEX IF NOT EXISTS idx_transport_journal_pending
    ON transport_journal(queue_id, stream, direction, status, seq);
CREATE INDEX IF NOT EXISTS idx_transport_journal_cleanup
    ON transport_journal(status, updated_at);

CREATE TABLE IF NOT EXISTS agent_registration_tokens (
    token_hash TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    expires_at TIMESTAMPTZ,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_registration_tokens_owner ON agent_registration_tokens(owner_user_id);

CREATE TABLE IF NOT EXISTS node_registration_sessions (
    registration_id TEXT PRIMARY KEY,
    pair_code TEXT UNIQUE NOT NULL,
    poll_token_hash TEXT UNIQUE NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    owner_user_id TEXT REFERENCES users(user_id),
    node_id TEXT REFERENCES nodes(node_id),
    requested_name TEXT NOT NULL DEFAULT '',
    requested_hostname TEXT NOT NULL,
    requested_machine_type TEXT NOT NULL DEFAULT '',
    requested_os TEXT NOT NULL DEFAULT 'unknown',
    requested_arch TEXT NOT NULL DEFAULT '',
    requested_paxd_version TEXT NOT NULL DEFAULT '',
    requested_api_endpoint TEXT NOT NULL DEFAULT 'http://localhost:8642',
    requested_metadata JSONB,
    request_ip TEXT NOT NULL DEFAULT '',
    request_city TEXT NOT NULL DEFAULT '',
    request_country TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ
);

ALTER TABLE node_registration_sessions ADD COLUMN IF NOT EXISTS request_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE node_registration_sessions ADD COLUMN IF NOT EXISTS request_city TEXT NOT NULL DEFAULT '';
ALTER TABLE node_registration_sessions ADD COLUMN IF NOT EXISTS request_country TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_node_registration_sessions_pair_code
    ON node_registration_sessions(pair_code);
CREATE INDEX IF NOT EXISTS idx_node_registration_sessions_poll
    ON node_registration_sessions(registration_id, poll_token_hash);
CREATE INDEX IF NOT EXISTS idx_node_registration_sessions_owner
    ON node_registration_sessions(owner_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_node_registration_sessions_expires
    ON node_registration_sessions(expires_at);

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

CREATE TABLE IF NOT EXISTS paxl_device_login_sessions (
    login_id TEXT PRIMARY KEY,
    user_code TEXT UNIQUE NOT NULL,
    poll_token_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    client_name TEXT NOT NULL DEFAULT '',
    owner_user_id TEXT REFERENCES users(user_id),
    user_api_key_id TEXT REFERENCES user_api_keys(key_id),
    node_id TEXT REFERENCES nodes(node_id),
    api_key TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ
);

ALTER TABLE paxl_device_login_sessions ADD COLUMN IF NOT EXISTS node_id TEXT REFERENCES nodes(node_id);

CREATE INDEX IF NOT EXISTS idx_paxl_device_login_sessions_poll
    ON paxl_device_login_sessions(login_id, poll_token_hash);
CREATE INDEX IF NOT EXISTS idx_paxl_device_login_sessions_user_code
    ON paxl_device_login_sessions(user_code);
CREATE INDEX IF NOT EXISTS idx_paxl_device_login_sessions_owner
    ON paxl_device_login_sessions(owner_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_paxl_device_login_sessions_expires
    ON paxl_device_login_sessions(expires_at);

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
    product TEXT NOT NULL DEFAULT 'paxd',
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

ALTER TABLE paxd_artifacts ADD COLUMN IF NOT EXISTS product TEXT NOT NULL DEFAULT 'paxd';
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
CREATE INDEX IF NOT EXISTS idx_paxd_artifacts_product_platform_created
    ON paxd_artifacts(product, platform, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_paxd_artifacts_tags
    ON paxd_artifacts USING GIN(tags)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS knowledge_capsules (
    capsule_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    source_session_id TEXT NOT NULL,
    source_agent_id TEXT NOT NULL,
    source_node_id TEXT NOT NULL DEFAULT '',
    created_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    keyword TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    suggested_skills_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    references_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    open_questions_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    risks_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    redactions_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL DEFAULT 'active',
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    original_estimated_chars BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS source_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS source_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS source_node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS keyword TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS content TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS suggested_skills_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS references_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS open_questions_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS risks_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS redactions_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS truncated BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS original_estimated_chars BIGINT NOT NULL DEFAULT 0;
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE knowledge_capsules ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_knowledge_capsules_owner_status
    ON knowledge_capsules(owner_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_knowledge_capsules_source_session
    ON knowledge_capsules(owner_user_id, source_session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_knowledge_capsules_keyword
    ON knowledge_capsules(owner_user_id, keyword, created_at DESC);

CREATE TABLE IF NOT EXISTS envelopes (
    envelope_id TEXT PRIMARY KEY,
    sender_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    sender_email TEXT NOT NULL,
    recipient_user_id TEXT REFERENCES users(user_id),
    recipient_email TEXT NOT NULL,
    payload_type TEXT NOT NULL,
    payload_json JSONB NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accepted_at TIMESTAMPTZ,
    archived_at TIMESTAMPTZ
);

ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS sender_user_id TEXT REFERENCES users(user_id);
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS sender_email TEXT NOT NULL DEFAULT '';
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS recipient_user_id TEXT REFERENCES users(user_id);
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS recipient_email TEXT NOT NULL DEFAULT '';
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS payload_type TEXT NOT NULL DEFAULT '';
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS payload_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS message TEXT NOT NULL DEFAULT '';
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ;
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_envelopes_recipient_user_status
    ON envelopes(recipient_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_envelopes_recipient_email_status
    ON envelopes(recipient_email, status, created_at DESC)
    WHERE recipient_user_id IS NULL OR recipient_user_id = '';
CREATE INDEX IF NOT EXISTS idx_envelopes_sender_created
    ON envelopes(sender_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS friends (
    friend_id TEXT PRIMARY KEY,
    requester_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    requester_email TEXT NOT NULL DEFAULT '',
    requester_alias TEXT NOT NULL DEFAULT '',
    recipient_user_id TEXT REFERENCES users(user_id),
    recipient_email TEXT NOT NULL DEFAULT '',
    recipient_alias TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accepted_at TIMESTAMPTZ,
    removed_at TIMESTAMPTZ,
    blocked_at TIMESTAMPTZ
);

ALTER TABLE friends ADD COLUMN IF NOT EXISTS requester_user_id TEXT REFERENCES users(user_id);
ALTER TABLE friends ADD COLUMN IF NOT EXISTS requester_email TEXT NOT NULL DEFAULT '';
ALTER TABLE friends ADD COLUMN IF NOT EXISTS requester_alias TEXT NOT NULL DEFAULT '';
ALTER TABLE friends ADD COLUMN IF NOT EXISTS recipient_user_id TEXT REFERENCES users(user_id);
ALTER TABLE friends ADD COLUMN IF NOT EXISTS recipient_email TEXT NOT NULL DEFAULT '';
ALTER TABLE friends ADD COLUMN IF NOT EXISTS recipient_alias TEXT NOT NULL DEFAULT '';
ALTER TABLE friends ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE friends ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE friends ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ;
ALTER TABLE friends ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ;
ALTER TABLE friends ADD COLUMN IF NOT EXISTS blocked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_friends_requester_status
    ON friends(requester_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_friends_recipient_user_status
    ON friends(recipient_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_friends_recipient_email_status
    ON friends(recipient_email, status, created_at DESC)
    WHERE recipient_user_id IS NULL OR recipient_user_id = '';
CREATE INDEX IF NOT EXISTS idx_friends_requester_alias
    ON friends(requester_user_id, requester_alias)
    WHERE status = 'accepted';
CREATE INDEX IF NOT EXISTS idx_friends_recipient_alias
    ON friends(recipient_user_id, recipient_alias)
    WHERE status = 'accepted';

CREATE TABLE IF NOT EXISTS teams (
    team_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE teams ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE teams ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE teams ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE teams ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE teams ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE teams ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_teams_owner_status
    ON teams(owner_user_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS team_members (
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    invited_by_user_id TEXT REFERENCES users(user_id),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    removed_at TIMESTAMPTZ,
    removed_by_user_id TEXT REFERENCES users(user_id),
    PRIMARY KEY (team_id, user_id)
);

ALTER TABLE team_members ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'member';
ALTER TABLE team_members ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE team_members ADD COLUMN IF NOT EXISTS invited_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_members ADD COLUMN IF NOT EXISTS joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_members ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ;
ALTER TABLE team_members ADD COLUMN IF NOT EXISTS removed_by_user_id TEXT REFERENCES users(user_id);

CREATE INDEX IF NOT EXISTS idx_team_members_user_status
    ON team_members(user_id, status, joined_at DESC);
CREATE INDEX IF NOT EXISTS idx_team_members_team_status
    ON team_members(team_id, status, joined_at);

CREATE TABLE IF NOT EXISTS team_invites (
    invite_id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    recipient_user_id TEXT REFERENCES users(user_id),
    role TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    invited_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accepted_at TIMESTAMPTZ,
    declined_at TIMESTAMPTZ,
    canceled_at TIMESTAMPTZ
);

ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS team_id TEXT REFERENCES teams(team_id);
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS recipient_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'member';
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS invited_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ;
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS declined_at TIMESTAMPTZ;
ALTER TABLE team_invites ADD COLUMN IF NOT EXISTS canceled_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_team_invites_recipient_user_status
    ON team_invites(recipient_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_team_invites_email_status
    ON team_invites(email, status, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_invites_pending_email
    ON team_invites(team_id, email)
    WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS team_agents (
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    agent_owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    identity TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'general',
    display_name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    added_by_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    removed_at TIMESTAMPTZ,
    removed_by_user_id TEXT REFERENCES users(user_id),
    PRIMARY KEY (team_id, agent_id)
);

ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS agent_owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS identity TEXT NOT NULL DEFAULT '';
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'general';
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS added_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS added_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ;
ALTER TABLE team_agents ADD COLUMN IF NOT EXISTS removed_by_user_id TEXT REFERENCES users(user_id);

CREATE INDEX IF NOT EXISTS idx_team_agents_owner_active
    ON team_agents(agent_owner_user_id, team_id)
    WHERE removed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_team_agents_team_active
    ON team_agents(team_id, added_at)
    WHERE removed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_team_agents_identity_active
    ON team_agents(team_id, identity)
    WHERE removed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_team_agents_role_active
    ON team_agents(team_id, role)
    WHERE removed_at IS NULL;

CREATE TABLE IF NOT EXISTS team_audit_events (
    event_id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    actor_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    target_user_id TEXT REFERENCES users(user_id),
    target_agent_id TEXT REFERENCES agents(agent_id),
    target_invite_id TEXT REFERENCES team_invites(invite_id),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS team_id TEXT REFERENCES teams(team_id);
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS actor_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT '';
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS target_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS target_agent_id TEXT REFERENCES agents(agent_id);
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS target_invite_id TEXT REFERENCES team_invites(invite_id);
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE team_audit_events ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_team_audit_events_team_created
    ON team_audit_events(team_id, created_at DESC);

CREATE TABLE IF NOT EXISTS team_memex_documents (
    document_id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    tags_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    body_md TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ,
    UNIQUE(team_id, path)
);

ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS team_id TEXT REFERENCES teams(team_id);
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS path TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS tags_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS body_md TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_memex_documents ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_memex_documents_team_path
    ON team_memex_documents(team_id, path);
CREATE INDEX IF NOT EXISTS idx_team_memex_documents_active
    ON team_memex_documents(team_id, path)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS session_knowledge_injections (
    injection_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    capsule_id TEXT NOT NULL REFERENCES knowledge_capsules(capsule_id),
    target_session_id TEXT NOT NULL,
    target_agent_id TEXT NOT NULL,
    target_node_id TEXT NOT NULL DEFAULT '',
    created_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    delivered_as_user_id TEXT NOT NULL DEFAULT '',
    delivery_method TEXT NOT NULL DEFAULT 'mailbox_steer',
    delivery_message_id TEXT NOT NULL DEFAULT '',
    delivery_message_type TEXT NOT NULL DEFAULT 'system_handoff',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    error TEXT NOT NULL DEFAULT ''
);

ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS owner_user_id TEXT REFERENCES users(user_id);
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS capsule_id TEXT REFERENCES knowledge_capsules(capsule_id);
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS target_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS target_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS target_node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS delivered_as_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS delivery_method TEXT NOT NULL DEFAULT 'mailbox_steer';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS delivery_message_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS delivery_message_type TEXT NOT NULL DEFAULT 'system_handoff';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS failed_at TIMESTAMPTZ;
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;
ALTER TABLE session_knowledge_injections ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_session_knowledge_injections_capsule
    ON session_knowledge_injections(owner_user_id, capsule_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_knowledge_injections_target_session
    ON session_knowledge_injections(owner_user_id, target_session_id, created_at DESC);

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

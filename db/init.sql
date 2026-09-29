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

CREATE TABLE IF NOT EXISTS projects (
    project_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    parent_project_id TEXT,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_projects_owner
    ON projects(owner_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_projects_parent
    ON projects(parent_project_id);

CREATE TABLE IF NOT EXISTS project_targets (
    target_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    cwd TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_project_targets_project
    ON project_targets(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_project_targets_agent
    ON project_targets(agent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_targets_one_default
    ON project_targets(project_id)
    WHERE is_default = TRUE AND enabled = TRUE;

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
    next_acp_request_id BIGINT NOT NULL DEFAULT 0,
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
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS deleted_by_user_id TEXT REFERENCES users(user_id);
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
ALTER TABLE agents ADD COLUMN IF NOT EXISTS next_acp_request_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS deleted_by_user_id TEXT REFERENCES users(user_id);

CREATE INDEX IF NOT EXISTS idx_agents_owner ON agents(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_agents_node ON agents(node_id);
CREATE INDEX IF NOT EXISTS idx_nodes_owner_active ON nodes(owner_user_id, registered_at)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agents_node_active ON agents(node_id, registered_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_connection_epochs (
    agent_id TEXT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    connection_epoch BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS agent_commands (
    id BIGSERIAL PRIMARY KEY,
    command_id TEXT NOT NULL UNIQUE,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    protocol_version INTEGER NOT NULL,
    cipher_version INTEGER NOT NULL,
    key_epoch BIGINT NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    delivered_epoch BIGINT,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_epoch BIGINT,
    expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_agent_commands_pending
    ON agent_commands(agent_id, id)
    WHERE acknowledged_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_events (
    cursor BIGSERIAL PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    local_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    protocol_version INTEGER NOT NULL,
    cipher_version INTEGER NOT NULL,
    key_epoch BIGINT NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, local_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_events_session_cursor
    ON agent_events(owner_user_id, session_id, cursor);

CREATE TABLE IF NOT EXISTS e2ee_messages (
    id BIGSERIAL PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    record_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    protocol_version INTEGER NOT NULL,
    cipher_version INTEGER NOT NULL,
    key_epoch BIGINT NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, session_id, message_id),
    UNIQUE(agent_id, record_id)
);

CREATE INDEX IF NOT EXISTS idx_e2ee_messages_session_page
    ON e2ee_messages(owner_user_id, session_id, id DESC);

-- Transcript ordering key for e2ee, mirroring plaintext messages. Cleartext so
-- ordering/cursor works without decryption. conversation_seq is reserved for
-- future e2ee conversations (none today).
ALTER TABLE e2ee_messages ADD COLUMN IF NOT EXISTS session_seq BIGINT;
ALTER TABLE e2ee_messages ADD COLUMN IF NOT EXISTS conversation_seq BIGINT;
CREATE INDEX IF NOT EXISTS idx_e2ee_messages_session_seq
    ON e2ee_messages(owner_user_id, session_id, session_seq)
    WHERE session_seq IS NOT NULL;

CREATE TABLE IF NOT EXISTS e2ee_message_parts (
    id BIGSERIAL PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    part_index INTEGER NOT NULL,
    revision BIGINT NOT NULL,
    record_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    protocol_version INTEGER NOT NULL,
    cipher_version INTEGER NOT NULL,
    key_epoch BIGINT NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, session_id, message_id, part_index),
    UNIQUE(agent_id, record_id),
    FOREIGN KEY (agent_id, session_id, message_id)
        REFERENCES e2ee_messages(agent_id, session_id, message_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_e2ee_message_parts_message
    ON e2ee_message_parts(agent_id, session_id, message_id, part_index);

CREATE TABLE IF NOT EXISTS e2ee_pairing_requests (
    pairing_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    device_id TEXT NOT NULL,
    device_name TEXT NOT NULL,
    key_epoch BIGINT NOT NULL,
    recipient_public_key BYTEA NOT NULL,
    secret_commitment BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_e2ee_pairing_requests_pending
    ON e2ee_pairing_requests(owner_user_id, agent_id, created_at)
    WHERE completed_at IS NULL;

CREATE TABLE IF NOT EXISTS e2ee_key_packages (
    pairing_id TEXT NOT NULL REFERENCES e2ee_pairing_requests(pairing_id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    device_id TEXT NOT NULL,
    key_epoch BIGINT NOT NULL,
    recipient_public_key BYTEA NOT NULL,
    sender_ephemeral_public_key BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(agent_id, device_id, key_epoch)
);

CREATE INDEX IF NOT EXISTS idx_e2ee_key_packages_owner_device
    ON e2ee_key_packages(owner_user_id, device_id, agent_id, key_epoch);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id BIGSERIAL PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    session_name TEXT,
    custom_session_name TEXT,
    agent_type TEXT,
    native_id TEXT,
    primary_project_id TEXT,
    preview TEXT,
    workspace_roots JSONB NOT NULL DEFAULT '[]'::jsonb,
    source TEXT,
    transport TEXT NOT NULL DEFAULT 'manager',
    status TEXT NOT NULL DEFAULT 'idle',
    current_task TEXT,
    last_message_at TIMESTAMPTZ,
    last_user_message_at TIMESTAMPTZ,
    message_count INTEGER NOT NULL DEFAULT 0,
    token_input BIGINT NOT NULL DEFAULT 0,
    token_output BIGINT NOT NULL DEFAULT 0,
    token_total BIGINT NOT NULL DEFAULT 0,
    model TEXT,
    run_id TEXT,
    run_status TEXT,
    runtime_status TEXT NOT NULL DEFAULT 'idle',
    runtime_turn_instance_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ,
    UNIQUE(agent_id, session_id)
);

ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS session_name TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS custom_session_name TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS node_id TEXT REFERENCES nodes(node_id);
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS agent_type TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS native_id TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS primary_project_id TEXT;
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS project_id;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS preview TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS workspace_roots JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS source TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS transport TEXT NOT NULL DEFAULT 'manager';
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS current_task TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS last_message_at TIMESTAMPTZ;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS last_user_message_at TIMESTAMPTZ;
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
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS runtime_status TEXT NOT NULL DEFAULT 'idle';
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS runtime_turn_instance_id TEXT;
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

UPDATE agent_sessions AS session
SET transport = 'e2ee'
WHERE session.transport <> 'e2ee'
  AND (
    EXISTS (
      SELECT 1
      FROM agent_commands AS command
      WHERE command.agent_id = session.agent_id
        AND command.session_id = session.session_id
    )
    OR EXISTS (
      SELECT 1
      FROM agent_events AS encrypted_event
      WHERE encrypted_event.agent_id = session.agent_id
        AND encrypted_event.session_id = session.session_id
    )
    OR EXISTS (
      SELECT 1
      FROM e2ee_messages AS message
      WHERE message.agent_id = session.agent_id
        AND message.session_id = session.session_id
    )
  );

CREATE INDEX IF NOT EXISTS idx_sessions_agent ON agent_sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_sessions_agent_native ON agent_sessions(agent_id, native_id)
    WHERE native_id IS NOT NULL AND native_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_status ON agent_sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_active_activity
    ON agent_sessions(last_user_message_at DESC, last_message_at DESC, updated_at DESC)
    WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_primary_project
    ON agent_sessions(primary_project_id)
    WHERE primary_project_id IS NOT NULL AND primary_project_id <> '';

CREATE TABLE IF NOT EXISTS node_runtime_fences (
    node_id TEXT PRIMARY KEY REFERENCES nodes(node_id) ON DELETE CASCADE,
    connection_fence TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS agent_runtime_snapshot_heads (
    agent_id TEXT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    connection_fence TEXT,
    last_sequence BIGINT,
    runtime_authority TEXT NOT NULL DEFAULT 'frames',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (runtime_authority IN ('frames', 'snapshot'))
);

CREATE TABLE IF NOT EXISTS agent_runtime_identities (
    agent_id TEXT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    report_epoch TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL,
    connection_id TEXT NOT NULL,
    report_generation BIGINT NOT NULL,
    protocol_version INTEGER NOT NULL DEFAULT 0,
    acp_agent_name TEXT NOT NULL DEFAULT '',
    acp_agent_title TEXT NOT NULL DEFAULT '',
    acp_agent_version TEXT NOT NULL DEFAULT '',
    runtime_name TEXT NOT NULL DEFAULT '',
    runtime_version TEXT NOT NULL DEFAULT '',
    runtime_build TEXT NOT NULL DEFAULT '',
    runtime_channel TEXT NOT NULL DEFAULT '',
    identity_fingerprint TEXT NOT NULL,
    configuration_fingerprint TEXT NOT NULL DEFAULT '',
    command_fingerprint TEXT NOT NULL DEFAULT '',
    client_profile_hash TEXT NOT NULL DEFAULT '',
    worker_result_hash TEXT NOT NULL DEFAULT '',
    pool_consistency TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE agent_runtime_identities
    ADD COLUMN IF NOT EXISTS report_epoch TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runtime_identities
    ADD COLUMN IF NOT EXISTS client_profile_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_runtime_identities
    ADD COLUMN IF NOT EXISTS configuration_fingerprint TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_agent_runtime_identities_fingerprint
    ON agent_runtime_identities(identity_fingerprint);

CREATE TABLE IF NOT EXISTS agent_permission_profiles (
    profile_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'draft',
    owner_user_id TEXT REFERENCES users(user_id) ON DELETE CASCADE,
    agent_type TEXT NOT NULL DEFAULT '',
    acp_agent_name TEXT NOT NULL DEFAULT '',
    acp_agent_version_constraint TEXT NOT NULL DEFAULT '',
    runtime_name TEXT NOT NULL DEFAULT '',
    runtime_version_constraint TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    definition JSONB NOT NULL,
    source TEXT NOT NULL DEFAULT 'admin',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (profile_id, revision),
    CHECK (status IN ('draft', 'active', 'retired'))
);

CREATE INDEX IF NOT EXISTS idx_agent_permission_profiles_match
    ON agent_permission_profiles(status, agent_type, acp_agent_name, priority DESC);
CREATE INDEX IF NOT EXISTS idx_agent_permission_profiles_owner
    ON agent_permission_profiles(owner_user_id, status, priority DESC);

INSERT INTO agent_permission_profiles (
    profile_id,
    revision,
    status,
    agent_type,
    acp_agent_name,
    acp_agent_version_constraint,
    runtime_name,
    runtime_version_constraint,
    priority,
    definition,
    source
) VALUES (
    'codex-acp.permissions',
    1,
    'active',
    'codex',
    '@agentclientprotocol/codex-acp',
    '*',
    'codex',
    '*',
    100,
    '{"binding":{"kind":"config_option","config_id":"mode","category":"mode"},"default_choice_id":"agent:mode:agent","native_choices":[{"choice_id":"agent:mode:read-only","label":"Read-only","value":"read-only","risk":"read_only"},{"choice_id":"agent:mode:agent","label":"Agent","value":"agent","risk":"workspace_write"},{"choice_id":"agent:mode:agent-full-access","label":"Agent (full access)","value":"agent-full-access","risk":"host_full_access","requires_confirmation":true}]}'::jsonb,
    'builtin'
)
ON CONFLICT (profile_id, revision) DO NOTHING;

CREATE TABLE IF NOT EXISTS agent_permission_observations (
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    identity_fingerprint TEXT NOT NULL,
    catalog_revision BIGINT NOT NULL DEFAULT 1,
    catalog_hash TEXT NOT NULL,
    catalog JSONB NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (agent_id, identity_fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_agent_permission_observations_expiry
    ON agent_permission_observations(agent_id, expires_at DESC);

CREATE TABLE IF NOT EXISTS agent_native_session_bindings (
    agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    native_session_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (agent_id, native_session_id),
    UNIQUE (agent_id, session_id),
    FOREIGN KEY (agent_id, session_id)
        REFERENCES agent_sessions(agent_id, session_id) ON DELETE CASCADE
);

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

CREATE TABLE IF NOT EXISTS agent_audit_events (
    event_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id),
    node_id TEXT REFERENCES nodes(node_id),
    agent_id TEXT REFERENCES agents(agent_id) ON DELETE CASCADE,
    session_id TEXT,
    turn_id TEXT,
    message_id TEXT,
    approval_id TEXT,
    event_type TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_id TEXT NOT NULL,
    event_key TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    tool_input JSONB,
    reason TEXT NOT NULL DEFAULT '',
    risk_level TEXT NOT NULL DEFAULT '',
    approval_status TEXT NOT NULL DEFAULT '',
    decision TEXT NOT NULL DEFAULT '',
    decision_scope TEXT NOT NULL DEFAULT '',
    decided_by_user_id TEXT NOT NULL DEFAULT '',
    decided_at TIMESTAMPTZ,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(source_type, source_id, event_type, event_key)
);

CREATE INDEX IF NOT EXISTS idx_agent_audit_owner_time ON agent_audit_events(owner_user_id, occurred_at DESC, event_id);
CREATE INDEX IF NOT EXISTS idx_agent_audit_agent_time ON agent_audit_events(agent_id, occurred_at DESC, event_id);
CREATE INDEX IF NOT EXISTS idx_agent_audit_session_time ON agent_audit_events(session_id, occurred_at DESC, event_id);
CREATE INDEX IF NOT EXISTS idx_agent_audit_approval ON agent_audit_events(approval_id);
CREATE INDEX IF NOT EXISTS idx_agent_audit_event_type ON agent_audit_events(event_type, occurred_at DESC);

CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    message_id TEXT UNIQUE NOT NULL,
    conversation_id TEXT,
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

ALTER TABLE messages ADD COLUMN IF NOT EXISTS conversation_id TEXT;

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
CREATE INDEX IF NOT EXISTS idx_messages_agent_session_id ON messages(agent_id, session_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_conversation_created ON messages(conversation_id, created_at, id)
    WHERE conversation_id IS NOT NULL AND conversation_id <> '';
CREATE INDEX IF NOT EXISTS idx_message_parts_message ON message_parts(message_id, part_index);

-- Transcript ordering key (seq refactor). session_seq / conversation_seq are
-- scope-monotonic, assigned once at row creation and immutable. They are the
-- single ordering/cursor key for history/conversation/events and are decoupled
-- from the reliablemq transport offset.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS session_seq BIGINT;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS conversation_seq BIGINT;
CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, session_seq)
    WHERE session_seq IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_messages_conversation_seq ON messages(conversation_id, conversation_seq)
    WHERE conversation_id IS NOT NULL AND conversation_id <> '' AND conversation_seq IS NOT NULL;

-- One-time backfill: assign session_seq to existing rows in ascending id order
-- (historical receipt order). New rows compute MAX(seq)+1 at insert time, so no
-- counter table is needed. Idempotent: only fills NULLs.
WITH ranked AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id) AS rn
    FROM messages
    WHERE session_seq IS NULL AND session_id IS NOT NULL AND session_id <> ''
)
UPDATE messages AS m
SET session_seq = ranked.rn
FROM ranked
WHERE m.id = ranked.id;

WITH ranked AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY conversation_id ORDER BY id) AS rn
    FROM messages
    WHERE conversation_seq IS NULL AND conversation_id IS NOT NULL AND conversation_id <> ''
)
UPDATE messages AS m
SET conversation_seq = ranked.rn
FROM ranked
WHERE m.id = ranked.id;

-- Existing agents may already have manager-generated ACP request IDs recorded
-- in prompt history. Start above that high-water mark so the first request
-- after deployment cannot overwrite an older prompt.
UPDATE agents AS agent
SET next_acp_request_id = prompt_ids.max_request_id
FROM (
    SELECT
        agent_id,
        MAX((raw_json ->> 'id')::BIGINT) AS max_request_id
    FROM messages
    WHERE message_type IN ('user_message', 'pax:user_message')
      AND raw_json ->> 'id' ~ '^[0-9]+$'
    GROUP BY agent_id
) AS prompt_ids
WHERE agent.agent_id = prompt_ids.agent_id
  AND agent.next_acp_request_id < prompt_ids.max_request_id;

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
UPDATE transport_journal
SET error_message = error
WHERE error_message = ''
  AND error IS NOT NULL
  AND error <> '';
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

CREATE TABLE IF NOT EXISTS transport_queue_state (
    queue_id TEXT NOT NULL,
    stream TEXT NOT NULL,
    next_outbound_seq BIGINT NOT NULL DEFAULT 1,
    inbound_acked_through BIGINT NOT NULL DEFAULT 0,
    inbound_applied_through BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (queue_id, stream)
);

ALTER TABLE transport_queue_state
    ADD COLUMN IF NOT EXISTS inbound_acked_through BIGINT NOT NULL DEFAULT 0;

INSERT INTO transport_queue_state (
    queue_id,
    stream,
    next_outbound_seq,
    inbound_acked_through,
    inbound_applied_through,
    created_at,
    updated_at
)
SELECT
    queue_id,
    stream,
    COALESCE(MAX(seq) FILTER (WHERE direction = 'outbound'), 0) + 1,
    0,
    0,
    MIN(created_at),
    NOW()
FROM transport_journal
GROUP BY queue_id, stream
ON CONFLICT (queue_id, stream) DO NOTHING;

WITH queue_candidates AS (
    SELECT queue_id, stream
    FROM transport_queue_state
    WHERE inbound_acked_through = 0
),
ranked_inbound AS (
    SELECT
        candidate.queue_id,
        candidate.stream,
        journal.seq,
        ROW_NUMBER() OVER (
            PARTITION BY candidate.queue_id, candidate.stream
            ORDER BY journal.seq
        ) AS ordinal
    FROM queue_candidates AS candidate
    LEFT JOIN transport_journal AS journal
        ON journal.queue_id = candidate.queue_id
        AND journal.stream = candidate.stream
        AND journal.direction = 'inbound'
        AND journal.status IN ('received', 'applied', 'rejected')
),
inbound_watermarks AS (
    SELECT
        queue_id,
        stream,
        COALESCE(
            MAX(seq) FILTER (
                WHERE seq = ordinal
            ),
            0
        ) AS acked_through
    FROM ranked_inbound
    GROUP BY queue_id, stream
)
UPDATE transport_queue_state AS state
SET
    inbound_acked_through = GREATEST(
        state.inbound_acked_through,
        inbound_watermarks.acked_through
    ),
    updated_at = NOW()
FROM inbound_watermarks
WHERE state.queue_id = inbound_watermarks.queue_id
    AND state.stream = inbound_watermarks.stream;

DROP INDEX IF EXISTS idx_transport_journal_pending;
CREATE INDEX IF NOT EXISTS idx_transport_journal_pending
    ON transport_journal(queue_id, stream, direction, status, seq);
CREATE INDEX IF NOT EXISTS idx_transport_journal_inbound_ack
    ON transport_journal(queue_id, stream, seq)
    WHERE direction = 'inbound'
        AND status IN ('received', 'applied', 'rejected');
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

CREATE TABLE IF NOT EXISTS user_attachments (
    attachment_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    conversation_id TEXT,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL,
    object TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    upload_status TEXT NOT NULL DEFAULT 'pending',
    upload_expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(bucket, object)
);

CREATE INDEX IF NOT EXISTS idx_user_attachments_owner_created
    ON user_attachments(owner_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_attachments_conversation_created
    ON user_attachments(conversation_id, created_at)
    WHERE conversation_id IS NOT NULL AND conversation_id <> '';

CREATE TABLE IF NOT EXISTS artifact_publications (
    publication_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued',
    artifact_id TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_artifact_publications_session_created
    ON artifact_publications(owner_user_id, session_id, created_at);

CREATE TABLE IF NOT EXISTS artifact_uploads (
    upload_id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    filename TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL,
    object TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE artifact_uploads ADD COLUMN IF NOT EXISTS artifact_id TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_uploads ADD COLUMN IF NOT EXISTS node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_uploads ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_artifact_uploads_owner_created
    ON artifact_uploads(owner_user_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifact_uploads_node_identity
    ON artifact_uploads(owner_user_id, session_id, filename, sha256)
    WHERE node_id <> '' AND sha256 <> '';

CREATE TABLE IF NOT EXISTS session_artifacts (
    artifact_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    title TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'available',
    session_id TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    source_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_session_artifacts_owner_session_created
    ON session_artifacts(owner_user_id, session_id, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_session_artifacts_owner_kind_created
    ON session_artifacts(owner_user_id, kind, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS session_artifact_contents (
    artifact_id TEXT NOT NULL REFERENCES session_artifacts(artifact_id) ON DELETE CASCADE,
    ref TEXT NOT NULL DEFAULT 'main',
    filename TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL DEFAULT '',
    object TEXT NOT NULL DEFAULT '',
    generation BIGINT NOT NULL DEFAULT 0,
    storage_uri TEXT NOT NULL DEFAULT '',
    text_content TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (artifact_id, ref)
);

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
    from_agent_id TEXT NOT NULL DEFAULT '',
    to_agent_id TEXT NOT NULL DEFAULT '',
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
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS from_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE envelopes ADD COLUMN IF NOT EXISTS to_agent_id TEXT NOT NULL DEFAULT '';
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
CREATE INDEX IF NOT EXISTS idx_envelopes_from_to_agent_created
    ON envelopes(from_agent_id, to_agent_id, created_at DESC)
    WHERE from_agent_id <> '' OR to_agent_id <> '';

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

CREATE TABLE IF NOT EXISTS team_memex_runs (
    run_id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    requested_by_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    executor_type TEXT NOT NULL DEFAULT 'dry_run',
    status TEXT NOT NULL DEFAULT 'pending',
    partial BOOLEAN NOT NULL DEFAULT FALSE,
    constraints_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    index_md TEXT NOT NULL DEFAULT '',
    validation_report_json JSONB,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS team_id TEXT REFERENCES teams(team_id);
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS requested_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS executor_type TEXT NOT NULL DEFAULT 'dry_run';
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS partial BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS constraints_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS index_md TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS validation_report_json JSONB;
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_memex_runs ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_team_memex_runs_team_started
    ON team_memex_runs(team_id, started_at DESC);

CREATE TABLE IF NOT EXISTS team_memex_run_attempts (
    attempt_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES team_memex_runs(run_id) ON DELETE CASCADE,
    team_id TEXT NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL,
    executor_type TEXT NOT NULL DEFAULT 'dry_run',
    status TEXT NOT NULL,
    manifest_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    validation_report_json JSONB,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS run_id TEXT REFERENCES team_memex_runs(run_id) ON DELETE CASCADE;
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS team_id TEXT REFERENCES teams(team_id);
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS attempt_number INTEGER NOT NULL DEFAULT 1;
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS executor_type TEXT NOT NULL DEFAULT 'dry_run';
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'validation_failed';
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS manifest_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS validation_report_json JSONB;
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE team_memex_run_attempts ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_memex_run_attempts_run_number
    ON team_memex_run_attempts(run_id, attempt_number);
CREATE INDEX IF NOT EXISTS idx_team_memex_run_attempts_team_run
    ON team_memex_run_attempts(team_id, run_id, attempt_number);

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
    raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    responded_at TIMESTAMPTZ,
    response_body JSONB,
    response_error TEXT NOT NULL DEFAULT ''
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
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS responded_at TIMESTAMPTZ;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS response_body JSONB;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS response_error TEXT NOT NULL DEFAULT '';

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

CREATE TABLE IF NOT EXISTS agent_profiles (
    profile_id TEXT PRIMARY KEY,
    owner_type TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    card_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    instructions_md TEXT NOT NULL DEFAULT '',
    default_model TEXT NOT NULL DEFAULT '',
    tool_policy_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active',
    created_by_user_id TEXT REFERENCES users(user_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS owner_type TEXT NOT NULL DEFAULT 'user';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS card_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS instructions_md TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS default_model TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS tool_policy_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_agent_profiles_owner_status
    ON agent_profiles(owner_type, owner_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_profiles_created_by
    ON agent_profiles(created_by_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS representative_agents (
    representative_agent_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES agent_profiles(profile_id),
    runtime_agent_id TEXT NOT NULL REFERENCES agents(agent_id),
    represents_type TEXT NOT NULL,
    represents_id TEXT NOT NULL,
    approval_policy_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_by_user_id TEXT REFERENCES users(user_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS profile_id TEXT REFERENCES agent_profiles(profile_id);
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS runtime_agent_id TEXT REFERENCES agents(agent_id);
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS represents_type TEXT NOT NULL DEFAULT 'user';
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS represents_id TEXT NOT NULL DEFAULT '';
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS approval_policy_id TEXT NOT NULL DEFAULT '';
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE representative_agents ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_representative_agents_profile
    ON representative_agents(profile_id, status);
CREATE INDEX IF NOT EXISTS idx_representative_agents_runtime
    ON representative_agents(runtime_agent_id, status);
CREATE INDEX IF NOT EXISTS idx_representative_agents_represents
    ON representative_agents(represents_type, represents_id, status);

CREATE TABLE IF NOT EXISTS conversations (
    conversation_id TEXT PRIMARY KEY,
    conversation_type TEXT NOT NULL,
    boundary_type TEXT NOT NULL DEFAULT 'personal',
    boundary_id TEXT NOT NULL DEFAULT '',
    history_policy TEXT NOT NULL DEFAULT 'full_history',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE conversations ADD COLUMN IF NOT EXISTS conversation_type TEXT NOT NULL DEFAULT 'direct';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS boundary_type TEXT NOT NULL DEFAULT 'personal';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS boundary_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS history_policy TEXT NOT NULL DEFAULT 'full_history';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_conversations_boundary_status
    ON conversations(boundary_type, boundary_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversations_type_status
    ON conversations(conversation_type, status, created_at DESC);

CREATE TABLE IF NOT EXISTS conversation_members (
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member',
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at TIMESTAMPTZ,
    PRIMARY KEY (conversation_id, user_id)
);

ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'member';
ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS left_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_conversation_members_user_active
    ON conversation_members(user_id, conversation_id)
    WHERE left_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_conversation_members_conversation_active
    ON conversation_members(conversation_id, joined_at)
    WHERE left_at IS NULL;

CREATE TABLE IF NOT EXISTS conversation_agent_bindings (
    binding_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    representative_agent_id TEXT NOT NULL REFERENCES representative_agents(representative_agent_id),
    relationship_type TEXT NOT NULL DEFAULT 'assistant',
    added_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    access_mode TEXT NOT NULL DEFAULT 'from_binding',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS conversation_id TEXT REFERENCES conversations(conversation_id) ON DELETE CASCADE;
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS representative_agent_id TEXT REFERENCES representative_agents(representative_agent_id);
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS relationship_type TEXT NOT NULL DEFAULT 'assistant';
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS added_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS access_mode TEXT NOT NULL DEFAULT 'from_binding';
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE conversation_agent_bindings ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_conversation_agent_bindings_conversation
    ON conversation_agent_bindings(conversation_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_agent_bindings_representative
    ON conversation_agent_bindings(representative_agent_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS conversation_agent_invocations (
    invocation_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    parent_invocation_id TEXT REFERENCES conversation_agent_invocations(invocation_id),
    source_representative_agent_id TEXT NOT NULL REFERENCES representative_agents(representative_agent_id),
    source_runtime_agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    source_session_id TEXT NOT NULL DEFAULT '',
    target_representative_agent_id TEXT NOT NULL REFERENCES representative_agents(representative_agent_id),
    target_runtime_agent_id TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    target_session_id TEXT NOT NULL DEFAULT '',
    receipt_token_hash TEXT NOT NULL DEFAULT '',
    representative_agent_id TEXT NOT NULL REFERENCES representative_agents(representative_agent_id),
    session_id TEXT NOT NULL DEFAULT '',
    requested_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    access_mode TEXT NOT NULL DEFAULT 'current_turn',
    selected_message_ids_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_turns INTEGER NOT NULL DEFAULT 1,
    remaining_turns INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS conversation_id TEXT REFERENCES conversations(conversation_id) ON DELETE CASCADE;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS parent_invocation_id TEXT REFERENCES conversation_agent_invocations(invocation_id);
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS source_representative_agent_id TEXT REFERENCES representative_agents(representative_agent_id);
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS source_runtime_agent_id TEXT REFERENCES agents(agent_id) ON DELETE CASCADE;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS source_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS target_representative_agent_id TEXT REFERENCES representative_agents(representative_agent_id);
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS target_runtime_agent_id TEXT REFERENCES agents(agent_id) ON DELETE CASCADE;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS target_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS receipt_token_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS representative_agent_id TEXT REFERENCES representative_agents(representative_agent_id);
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS requested_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS access_mode TEXT NOT NULL DEFAULT 'current_turn';
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS selected_message_ids_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS max_turns INTEGER NOT NULL DEFAULT 1;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS remaining_turns INTEGER NOT NULL DEFAULT 1;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE conversation_agent_invocations ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_conversation_agent_invocations_conversation
    ON conversation_agent_invocations(conversation_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_agent_invocations_representative
    ON conversation_agent_invocations(representative_agent_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_agent_invocations_session
    ON conversation_agent_invocations(session_id)
    WHERE session_id <> '';
CREATE INDEX IF NOT EXISTS idx_conversation_agent_invocations_parent
    ON conversation_agent_invocations(parent_invocation_id)
    WHERE parent_invocation_id IS NOT NULL AND parent_invocation_id <> '';
CREATE INDEX IF NOT EXISTS idx_conversation_agent_invocations_source
    ON conversation_agent_invocations(source_runtime_agent_id, source_session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_agent_invocations_target
    ON conversation_agent_invocations(target_runtime_agent_id, target_session_id, created_at DESC);
DROP INDEX IF EXISTS idx_active_inquiry_target_session;
WITH duplicate_active_invocations AS (
    SELECT invocation_id
    FROM (
        SELECT invocation_id,
               ROW_NUMBER() OVER (
                   PARTITION BY target_runtime_agent_id, target_session_id
                   ORDER BY created_at DESC, invocation_id DESC
               ) AS row_number
        FROM conversation_agent_invocations
        WHERE status = 'active'
          AND target_runtime_agent_id IS NOT NULL
          AND target_session_id <> ''
    ) ranked_active_invocations
    WHERE row_number > 1
)
UPDATE conversation_agent_invocations
SET status = 'expired',
    expires_at = COALESCE(expires_at, NOW())
WHERE invocation_id IN (SELECT invocation_id FROM duplicate_active_invocations);
CREATE UNIQUE INDEX IF NOT EXISTS idx_active_invocation_target_session
    ON conversation_agent_invocations(target_runtime_agent_id, target_session_id)
    WHERE status = 'active'
      AND target_runtime_agent_id IS NOT NULL
      AND target_session_id <> '';

CREATE TABLE IF NOT EXISTS access_grants (
    grant_id TEXT PRIMARY KEY,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    created_by_user_id TEXT REFERENCES users(user_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoked_by_user_id TEXT REFERENCES users(user_id),
    revocation_reason TEXT NOT NULL DEFAULT '',
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb
);

ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS resource_type TEXT NOT NULL DEFAULT '';
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS resource_id TEXT NOT NULL DEFAULT '';
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS subject_type TEXT NOT NULL DEFAULT '';
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS subject_id TEXT NOT NULL DEFAULT '';
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT '';
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS revoked_by_user_id TEXT REFERENCES users(user_id);
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS revocation_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE access_grants ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_access_grants_resource_action
    ON access_grants(resource_type, resource_id, action, revoked_at, expires_at);
CREATE INDEX IF NOT EXISTS idx_access_grants_subject_action
    ON access_grants(subject_type, subject_id, action, revoked_at, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_access_grants_unique_active
    ON access_grants(resource_type, resource_id, subject_type, subject_id, action)
    WHERE revoked_at IS NULL;

ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS conversation_id TEXT REFERENCES conversations(conversation_id);
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS profile_id TEXT REFERENCES agent_profiles(profile_id);
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS representative_agent_id TEXT REFERENCES representative_agents(representative_agent_id);
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS created_by_user_id TEXT REFERENCES users(user_id);

CREATE INDEX IF NOT EXISTS idx_sessions_conversation
    ON agent_sessions(conversation_id, created_at DESC)
    WHERE conversation_id IS NOT NULL AND conversation_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_profile
    ON agent_sessions(profile_id, created_at DESC)
    WHERE profile_id IS NOT NULL AND profile_id <> '';
CREATE INDEX IF NOT EXISTS idx_sessions_representative
    ON agent_sessions(representative_agent_id, created_at DESC)
    WHERE representative_agent_id IS NOT NULL AND representative_agent_id <> '';

CREATE TABLE IF NOT EXISTS agent_session_participants (
    session_id TEXT NOT NULL,
    participant_type TEXT NOT NULL,
    participant_id TEXT NOT NULL,
    role TEXT NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at TIMESTAMPTZ,
    PRIMARY KEY (session_id, participant_type, participant_id, role)
);

ALTER TABLE agent_session_participants ADD COLUMN IF NOT EXISTS added_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE agent_session_participants ADD COLUMN IF NOT EXISTS left_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_agent_session_participants_participant
    ON agent_session_participants(participant_type, participant_id, session_id)
    WHERE left_at IS NULL;

-- One bounded pending slot per session; completed work belongs in message history.
CREATE TABLE IF NOT EXISTS session_turn_queue (
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    input TEXT NOT NULL CHECK (octet_length(input) BETWEEN 1 AND 65536),
    state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'sending', 'uncertain')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (agent_id, session_id),
    FOREIGN KEY (agent_id, session_id) REFERENCES agent_sessions(agent_id, session_id)
        ON DELETE CASCADE
);

-- Runtime configuration overrides and feature gates. Applied at startup by
-- PostgresStore.EnsureSchema; every statement is idempotent. Credentials are
-- environment-only and are never stored here.
CREATE TABLE IF NOT EXISTS runtime_config (
  key          TEXT PRIMARY KEY,
  value_json   JSONB NOT NULL,
  version      BIGINT NOT NULL,
  updated_by   TEXT NOT NULL,
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS runtime_config_audit (
  id             BIGSERIAL PRIMARY KEY,
  key            TEXT NOT NULL,
  action         TEXT NOT NULL,
  old_display    TEXT,
  new_display    TEXT,
  old_value_json JSONB,
  version        BIGINT NOT NULL,
  actor          TEXT NOT NULL,
  reason         TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS runtime_config_audit_key_created_idx
  ON runtime_config_audit (key, created_at DESC);

CREATE TABLE IF NOT EXISTS runtime_config_revision (
  id       INT PRIMARY KEY CHECK (id = 1),
  revision BIGINT NOT NULL
);
INSERT INTO runtime_config_revision (id, revision) VALUES (1, 0) ON CONFLICT (id) DO NOTHING;

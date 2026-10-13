-- Separate from region assignment so new users still choose their region.
CREATE TABLE workos_identities (
  workos_user_id TEXT PRIMARY KEY NOT NULL,
  identity_key TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  CHECK (workos_user_id LIKE 'user_%'),
  CHECK (identity_key = lower(trim(identity_key)) AND instr(identity_key, '@') > 1)
);
CREATE TRIGGER preserve_workos_identity BEFORE UPDATE ON workos_identities
BEGIN SELECT RAISE(ABORT, 'workos identity binding is immutable'); END;

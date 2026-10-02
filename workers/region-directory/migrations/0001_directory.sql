CREATE TABLE user_regions (
  user_id TEXT PRIMARY KEY NOT NULL,
  identity_key TEXT NOT NULL UNIQUE,
  region TEXT NOT NULL CHECK (region IN ('us', 'hk')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  CHECK (length(user_id) > 4 AND identity_key = lower(trim(identity_key)) AND instr(identity_key, '@') > 1)
);
CREATE TRIGGER preserve_user_region BEFORE UPDATE ON user_regions
WHEN NEW.user_id != OLD.user_id OR NEW.identity_key != OLD.identity_key OR NEW.region != OLD.region
BEGIN SELECT RAISE(ABORT, 'user region identity is immutable'); END;

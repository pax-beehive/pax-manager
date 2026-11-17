CREATE TABLE IF NOT EXISTS hosting_state (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    document JSONB NOT NULL DEFAULT '{}'::jsonb
);

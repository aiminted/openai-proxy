CREATE TABLE IF NOT EXISTS upstream_keys (
    id          BIGSERIAL PRIMARY KEY,
    encrypted   BYTEA NOT NULL,
    prefix      TEXT NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at  TIMESTAMPTZ
);

-- At most one row may be active at a time. Older rows kept for audit.
CREATE UNIQUE INDEX IF NOT EXISTS upstream_keys_one_active
    ON upstream_keys (active) WHERE active;

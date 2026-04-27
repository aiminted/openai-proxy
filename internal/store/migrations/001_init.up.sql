CREATE TABLE IF NOT EXISTS api_keys (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prefix        TEXT NOT NULL UNIQUE,
    hash          TEXT NOT NULL,
    owner         TEXT NOT NULL,
    note          TEXT NOT NULL DEFAULT '',
    expires_at    TIMESTAMPTZ,
    rpm_limit     INTEGER,
    token_quota   BIGINT,
    dollar_quota  NUMERIC(14,8),
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS api_keys_owner_idx ON api_keys(owner);

CREATE TABLE IF NOT EXISTS usage_records (
    id              BIGSERIAL PRIMARY KEY,
    key_id          UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    request_id      TEXT,
    endpoint        TEXT NOT NULL,
    model           TEXT,
    streaming       BOOLEAN NOT NULL DEFAULT FALSE,
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    cached_tokens   INTEGER NOT NULL DEFAULT 0,
    cost_usd        NUMERIC(14,8) NOT NULL DEFAULT 0,
    status          INTEGER NOT NULL,
    duration_ms     INTEGER NOT NULL,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS usage_records_key_time_idx ON usage_records(key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_records_time_idx ON usage_records(created_at DESC);

CREATE TABLE IF NOT EXISTS quota_totals (
    key_id          UUID PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE,
    total_tokens    BIGINT NOT NULL DEFAULT 0,
    total_cost_usd  NUMERIC(14,8) NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

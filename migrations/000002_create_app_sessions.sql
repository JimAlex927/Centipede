CREATE TABLE IF NOT EXISTS app_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS app_sessions_user_id_idx
    ON app_sessions (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS app_sessions_active_expiry_idx
    ON app_sessions (expires_at)
    WHERE revoked_at IS NULL;

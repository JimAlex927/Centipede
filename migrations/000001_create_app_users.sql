CREATE TABLE IF NOT EXISTS app_users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    identity_issuer TEXT NOT NULL,
    identity_subject TEXT NOT NULL,
    email_snapshot TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (identity_issuer, identity_subject)
);

CREATE INDEX IF NOT EXISTS app_users_email_snapshot_idx
    ON app_users (lower(email_snapshot))
    WHERE email_snapshot IS NOT NULL;

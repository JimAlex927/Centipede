ALTER TABLE app_users
    ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS password_hash TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS app_users_local_email_idx
    ON app_users (lower(email_snapshot))
    WHERE identity_issuer = 'local' AND email_snapshot IS NOT NULL;

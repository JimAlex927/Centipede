CREATE TABLE IF NOT EXISTS vocabulary_entries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    language_tag TEXT NOT NULL,
    original_text TEXT NOT NULL,
    normalized_text TEXT NOT NULL,
    lemma TEXT,
    definition TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'learning', 'known', 'paused')),
    source_book TEXT NOT NULL DEFAULT '',
    source_chapter TEXT NOT NULL DEFAULT '',
    source_location TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS vocabulary_entries_user_created_idx
    ON vocabulary_entries (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS vocabulary_entries_user_language_idx
    ON vocabulary_entries (user_id, language_tag);
CREATE INDEX IF NOT EXISTS vocabulary_entries_user_status_idx
    ON vocabulary_entries (user_id, status);

CREATE TABLE IF NOT EXISTS vocabulary_contexts (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id BIGINT NOT NULL REFERENCES vocabulary_entries(id) ON DELETE CASCADE,
    context_text TEXT NOT NULL,
    source_location TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS vocabulary_contexts_entry_idx
    ON vocabulary_contexts (entry_id, created_at);

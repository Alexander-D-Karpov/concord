DROP INDEX IF EXISTS idx_messages_content_search;

CREATE INDEX IF NOT EXISTS idx_messages_fts
    ON messages USING gin(to_tsvector('simple', content))
    WHERE deleted_at IS NULL;

DROP INDEX IF EXISTS idx_dm_messages_content_search;

CREATE INDEX IF NOT EXISTS idx_dm_messages_fts
    ON dm_messages USING gin(to_tsvector('simple', content))
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_users_handle_lower ON users(lower(handle));
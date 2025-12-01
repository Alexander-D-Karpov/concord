CREATE INDEX IF NOT EXISTS idx_messages_content_search
    ON messages USING gin(to_tsvector('english', content));

CREATE INDEX IF NOT EXISTS idx_users_search
    ON users USING gin(to_tsvector('english', handle || ' ' || display_name));

CREATE OR REPLACE FUNCTION cleanup_expired_bans() RETURNS void AS $$
BEGIN
    DELETE FROM room_bans WHERE expires_at IS NOT NULL AND expires_at < NOW();
END;
$$ LANGUAGE plpgsql;
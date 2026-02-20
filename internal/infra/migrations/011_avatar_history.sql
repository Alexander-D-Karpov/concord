ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_thumbnail_url TEXT DEFAULT '';

CREATE TABLE IF NOT EXISTS user_avatars (
                                            id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                            user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                            full_url TEXT NOT NULL,
                                            thumbnail_url TEXT NOT NULL,
                                            original_filename TEXT DEFAULT '',
                                            size_bytes BIGINT DEFAULT 0,
                                            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_avatars_user_created ON user_avatars(user_id, created_at DESC);
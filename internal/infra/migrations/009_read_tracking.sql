-- Read tracking for rooms
CREATE TABLE IF NOT EXISTS room_read_status (
                                                user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                                last_read_message_id BIGINT NOT NULL DEFAULT 0,
                                                updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                                PRIMARY KEY (user_id, room_id)
);

CREATE INDEX IF NOT EXISTS idx_room_read_status_user ON room_read_status(user_id);
CREATE INDEX IF NOT EXISTS idx_room_read_status_room ON room_read_status(room_id);

-- Read tracking for DMs
CREATE TABLE IF NOT EXISTS dm_read_status (
                                              user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                              channel_id UUID NOT NULL REFERENCES dm_channels(id) ON DELETE CASCADE,
                                              last_read_message_id BIGINT NOT NULL DEFAULT 0,
                                              updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                              PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX IF NOT EXISTS idx_dm_read_status_user ON dm_read_status(user_id);
CREATE INDEX IF NOT EXISTS idx_dm_read_status_channel ON dm_read_status(channel_id);

-- Typing indicators
CREATE TABLE IF NOT EXISTS typing_indicators (
                                                 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                                 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                 room_id UUID REFERENCES rooms(id) ON DELETE CASCADE,
                                                 channel_id UUID REFERENCES dm_channels(id) ON DELETE CASCADE,
                                                 started_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                                 expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
                                                 CHECK (room_id IS NOT NULL OR channel_id IS NOT NULL),
                                                 CHECK (NOT (room_id IS NOT NULL AND channel_id IS NOT NULL))
);

-- Unique constraint for room typing (one entry per user per room)
CREATE UNIQUE INDEX IF NOT EXISTS idx_typing_room_unique
    ON typing_indicators(user_id, room_id)
    WHERE room_id IS NOT NULL;

-- Unique constraint for DM typing (one entry per user per channel)
CREATE UNIQUE INDEX IF NOT EXISTS idx_typing_channel_unique
    ON typing_indicators(user_id, channel_id)
    WHERE channel_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_typing_room ON typing_indicators(room_id) WHERE room_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_typing_channel ON typing_indicators(channel_id) WHERE channel_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_typing_expires ON typing_indicators(expires_at);

-- Cleanup function for expired typing indicators
CREATE OR REPLACE FUNCTION cleanup_expired_typing() RETURNS void AS $$
BEGIN
    DELETE FROM typing_indicators WHERE expires_at < NOW();
END;
$$ LANGUAGE plpgsql;
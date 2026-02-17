-- DM channels table
CREATE TABLE IF NOT EXISTS dm_channels (
                                           id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                           user1_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                           user2_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                           created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                           updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                           CONSTRAINT dm_channels_user_order CHECK (user1_id < user2_id),
                                           CONSTRAINT dm_channels_unique_pair UNIQUE (user1_id, user2_id)
);

CREATE INDEX IF NOT EXISTS idx_dm_channels_user1 ON dm_channels(user1_id);
CREATE INDEX IF NOT EXISTS idx_dm_channels_user2 ON dm_channels(user2_id);
CREATE INDEX IF NOT EXISTS idx_dm_channels_updated ON dm_channels(updated_at DESC);

-- DM participants table (Required by backend logic for membership checks)
CREATE TABLE IF NOT EXISTS dm_participants (
                                               channel_id UUID NOT NULL REFERENCES dm_channels(id) ON DELETE CASCADE,
                                               user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                               joined_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                               PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_dm_participants_user ON dm_participants(user_id);

-- Trigger to auto-populate participants from dm_channels (backward compatibility)
CREATE OR REPLACE FUNCTION sync_dm_participants() RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO dm_participants (channel_id, user_id) VALUES (NEW.id, NEW.user1_id) ON CONFLICT DO NOTHING;
    INSERT INTO dm_participants (channel_id, user_id) VALUES (NEW.id, NEW.user2_id) ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_dm_participants ON dm_channels;
CREATE TRIGGER trg_sync_dm_participants
    AFTER INSERT ON dm_channels
    FOR EACH ROW EXECUTE FUNCTION sync_dm_participants();

-- Backfill existing channels if any
INSERT INTO dm_participants (channel_id, user_id)
SELECT id, user1_id FROM dm_channels
ON CONFLICT DO NOTHING;

INSERT INTO dm_participants (channel_id, user_id)
SELECT id, user2_id FROM dm_channels
ON CONFLICT DO NOTHING;

-- DM messages use the same messages table with dm_channel_id instead of room_id
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dm_channel_id UUID REFERENCES dm_channels(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_messages_dm_channel ON messages(dm_channel_id) WHERE dm_channel_id IS NOT NULL;
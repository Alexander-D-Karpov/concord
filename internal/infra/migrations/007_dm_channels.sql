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

-- DM messages use the same messages table with dm_channel_id instead of room_id
ALTER TABLE messages ADD COLUMN IF NOT EXISTS dm_channel_id UUID REFERENCES dm_channels(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_messages_dm_channel ON messages(dm_channel_id) WHERE dm_channel_id IS NOT NULL;
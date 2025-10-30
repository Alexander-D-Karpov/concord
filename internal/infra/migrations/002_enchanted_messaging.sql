ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to_id BIGINT REFERENCES messages(id) ON DELETE SET NULL;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_count INTEGER DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_messages_reply_to ON messages(reply_to_id) WHERE reply_to_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS message_attachments (
                                                   id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                                   message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
                                                   url TEXT NOT NULL,
                                                   filename TEXT NOT NULL,
                                                   content_type TEXT,
                                                   size BIGINT,
                                                   width INTEGER,
                                                   height INTEGER,
                                                   created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_message_attachments_message ON message_attachments(message_id);

CREATE TABLE IF NOT EXISTS message_mentions (
                                                message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
                                                user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                                PRIMARY KEY (message_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_message_mentions_user ON message_mentions(user_id);

CREATE TABLE IF NOT EXISTS message_reactions (
                                                 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                                 message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
                                                 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                 emoji TEXT NOT NULL,
                                                 created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                                 UNIQUE (message_id, user_id, emoji)
);

CREATE INDEX IF NOT EXISTS idx_message_reactions_message ON message_reactions(message_id);
CREATE INDEX IF NOT EXISTS idx_message_reactions_user ON message_reactions(user_id);

CREATE TABLE IF NOT EXISTS pinned_messages (
                                               room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                               message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
                                               pinned_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                               pinned_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                               PRIMARY KEY (room_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_pinned_messages_room ON pinned_messages(room_id);

ALTER TABLE users ADD COLUMN IF NOT EXISTS status TEXT DEFAULT 'offline';
ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT;

CREATE INDEX IF NOT EXISTS idx_users_handle_search ON users USING gin(to_tsvector('english', handle || ' ' || display_name));

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS is_private BOOLEAN DEFAULT FALSE;

ALTER TABLE memberships ADD COLUMN IF NOT EXISTS nickname TEXT;

CREATE INDEX IF NOT EXISTS idx_rooms_created_at ON rooms(created_at DESC) WHERE deleted_at IS NULL;
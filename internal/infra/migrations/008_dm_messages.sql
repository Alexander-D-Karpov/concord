CREATE TABLE IF NOT EXISTS dm_messages (
                                           id BIGINT PRIMARY KEY,
                                           channel_id UUID NOT NULL REFERENCES dm_channels(id) ON DELETE CASCADE,
                                           author_id UUID NOT NULL REFERENCES users(id),
                                           content TEXT NOT NULL DEFAULT '',
                                           created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                           edited_at TIMESTAMPTZ,
                                           deleted_at TIMESTAMPTZ,
                                           reply_to_id BIGINT REFERENCES dm_messages(id) ON DELETE SET NULL,
                                           reply_count INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_dm_messages_channel ON dm_messages(channel_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_dm_messages_reply_to ON dm_messages(reply_to_id) WHERE reply_to_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS dm_message_attachments (
                                                      id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                                      message_id BIGINT NOT NULL REFERENCES dm_messages(id) ON DELETE CASCADE,
                                                      url TEXT NOT NULL,
                                                      filename TEXT NOT NULL,
                                                      content_type TEXT NOT NULL,
                                                      size BIGINT NOT NULL DEFAULT 0,
                                                      width INTEGER,
                                                      height INTEGER,
                                                      created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dm_message_attachments_message ON dm_message_attachments(message_id);

CREATE TABLE IF NOT EXISTS dm_message_reactions (
                                                    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                                    message_id BIGINT NOT NULL REFERENCES dm_messages(id) ON DELETE CASCADE,
                                                    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                    emoji TEXT NOT NULL,
                                                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                                    UNIQUE (message_id, user_id, emoji)
);

CREATE INDEX IF NOT EXISTS idx_dm_message_reactions_message ON dm_message_reactions(message_id);

CREATE TABLE IF NOT EXISTS dm_message_mentions (
                                                   message_id BIGINT NOT NULL REFERENCES dm_messages(id) ON DELETE CASCADE,
                                                   user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                   created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                                   PRIMARY KEY (message_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_dm_message_mentions_user ON dm_message_mentions(user_id);

CREATE TABLE IF NOT EXISTS dm_pinned_messages (
                                                  channel_id UUID NOT NULL REFERENCES dm_channels(id) ON DELETE CASCADE,
                                                  message_id BIGINT NOT NULL REFERENCES dm_messages(id) ON DELETE CASCADE,
                                                  pinned_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                  pinned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                                  PRIMARY KEY (channel_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_dm_pinned_messages_channel ON dm_pinned_messages(channel_id);

CREATE INDEX IF NOT EXISTS idx_dm_messages_content_search ON dm_messages USING gin(to_tsvector('english', content));

CREATE TABLE IF NOT EXISTS dm_calls (
                                        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                        channel_id UUID NOT NULL REFERENCES dm_channels(id) ON DELETE CASCADE,
                                        started_by UUID NOT NULL REFERENCES users(id),
                                        started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                        ended_at TIMESTAMPTZ,
                                        voice_server_id UUID REFERENCES voice_servers(id)
);

CREATE INDEX IF NOT EXISTS idx_dm_calls_channel ON dm_calls(channel_id);
CREATE INDEX IF NOT EXISTS idx_dm_calls_active ON dm_calls(channel_id) WHERE ended_at IS NULL;
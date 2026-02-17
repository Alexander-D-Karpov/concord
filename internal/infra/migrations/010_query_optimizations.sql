CREATE INDEX IF NOT EXISTS idx_dm_messages_channel_created
    ON dm_messages(channel_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_dm_message_attachments_message_created
    ON dm_message_attachments(message_id, created_at);

CREATE INDEX IF NOT EXISTS idx_dm_message_reactions_message_created
    ON dm_message_reactions(message_id, created_at);

CREATE INDEX IF NOT EXISTS idx_messages_room_created
    ON messages(room_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memberships_user_room
    ON memberships(user_id, room_id);

CREATE INDEX IF NOT EXISTS idx_friendships_combined
    ON friendships(user_id1, user_id2);

CREATE INDEX IF NOT EXISTS idx_friend_requests_to_user_status
    ON friend_requests(to_user_id, status)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_room_invites_invited_pending
    ON room_invites(invited_user_id, status)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_room_read_status_user_room
    ON room_read_status(user_id, room_id);

CREATE INDEX IF NOT EXISTS idx_dm_read_status_user_channel
    ON dm_read_status(user_id, channel_id);

CREATE INDEX IF NOT EXISTS idx_voice_servers_region_status_load
    ON voice_servers(region, status, load_score)
    WHERE status = 'online';
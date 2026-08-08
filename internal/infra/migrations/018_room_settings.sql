-- Per-room settings owned by the admin/moderation surface. Only NEW settings live
-- here; is_private and slow_mode_interval remain authoritative on the rooms table
-- and are kept in sync by UpdateRoomSettings.
CREATE TABLE IF NOT EXISTS room_settings (
    room_id               UUID PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
    who_can_invite        TEXT    NOT NULL DEFAULT 'member',   -- 'member' | 'moderator'
    who_can_post          TEXT    NOT NULL DEFAULT 'member',   -- 'member' | 'moderator'
    require_approval      BOOLEAN NOT NULL DEFAULT false,      -- advisory
    member_cap            INTEGER NOT NULL DEFAULT 0,          -- 0 = unlimited
    retention_days        INTEGER NOT NULL DEFAULT 0,          -- 0 = keep forever
    link_previews_enabled BOOLEAN NOT NULL DEFAULT true,       -- advisory
    gifs_enabled          BOOLEAN NOT NULL DEFAULT true,       -- advisory
    stickers_enabled      BOOLEAN NOT NULL DEFAULT true,       -- advisory
    updated_at            TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS room_word_filters (
    room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    word    TEXT NOT NULL,
    PRIMARY KEY (room_id, word)
);

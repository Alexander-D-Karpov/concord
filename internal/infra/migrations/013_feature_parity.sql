-- ===================== FORWARDING =====================
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_from_user_id UUID;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_from_user_name TEXT;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_from_room_id UUID;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_from_message_id BIGINT;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_original_timestamp TIMESTAMPTZ;

ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS forwarded_from_user_id UUID;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS forwarded_from_user_name TEXT;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS forwarded_from_channel_id UUID;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS forwarded_from_message_id BIGINT;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS forwarded_original_timestamp TIMESTAMPTZ;

-- ===================== SCHEDULED MESSAGES =====================
CREATE TABLE IF NOT EXISTS scheduled_messages (
                                                  id              BIGSERIAL PRIMARY KEY,
                                                  room_id         UUID REFERENCES rooms(id) ON DELETE CASCADE,
                                                  channel_id      UUID REFERENCES dm_channels(id) ON DELETE CASCADE,
                                                  author_id       UUID NOT NULL REFERENCES users(id),
                                                  content         TEXT NOT NULL DEFAULT '',
                                                  attachments_json JSONB,
                                                  reply_to_id     BIGINT,
                                                  scheduled_for   TIMESTAMPTZ NOT NULL,
                                                  status          TEXT NOT NULL DEFAULT 'pending'
                                                      CHECK (status IN ('pending','sent','failed','cancelled')),
                                                  created_at      TIMESTAMPTZ DEFAULT NOW(),
                                                  updated_at      TIMESTAMPTZ DEFAULT NOW(),
                                                  CHECK (room_id IS NOT NULL OR channel_id IS NOT NULL),
                                                  CHECK (NOT (room_id IS NOT NULL AND channel_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_scheduled_pending ON scheduled_messages(scheduled_for) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_scheduled_author ON scheduled_messages(author_id);

-- ===================== BOOKMARKS =====================
CREATE TABLE IF NOT EXISTS bookmarks (
                                         id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                         user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                         message_id  BIGINT NOT NULL,
                                         room_id     UUID,
                                         channel_id  UUID,
                                         note        TEXT DEFAULT '',
                                         tags        TEXT[] DEFAULT '{}',
                                         created_at  TIMESTAMPTZ DEFAULT NOW(),
                                         UNIQUE(user_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user ON bookmarks(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bookmarks_tags ON bookmarks USING GIN(tags);

-- ===================== EDIT HISTORY =====================
CREATE TABLE IF NOT EXISTS message_edits (
                                             id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                             message_id       BIGINT NOT NULL,
                                             previous_content TEXT NOT NULL,
                                             edited_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                             version          INT NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_message_edits_msg ON message_edits(message_id, version DESC);

ALTER TABLE messages ADD COLUMN IF NOT EXISTS edit_count INT DEFAULT 0;

CREATE TABLE IF NOT EXISTS dm_message_edits (
                                                id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                                message_id       BIGINT NOT NULL,
                                                previous_content TEXT NOT NULL,
                                                edited_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                                version          INT NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_dm_message_edits_msg ON dm_message_edits(message_id, version DESC);

ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS edit_count INT DEFAULT 0;

-- ===================== REPLY IMPROVEMENTS =====================
ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_quoted_content TEXT;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_mention_author BOOLEAN DEFAULT true;

ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS reply_quoted_content TEXT;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS reply_mention_author BOOLEAN DEFAULT true;

-- ===================== MEDIA GROUPS =====================
ALTER TABLE messages ADD COLUMN IF NOT EXISTS media_group_id TEXT;
ALTER TABLE dm_messages ADD COLUMN IF NOT EXISTS media_group_id TEXT;

CREATE TABLE IF NOT EXISTS media_index (
                                           id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                           message_id  BIGINT NOT NULL,
                                           room_id     UUID,
                                           channel_id  UUID,
                                           media_type  SMALLINT NOT NULL,
                                           file_url    TEXT NOT NULL,
                                           thumbnail_url TEXT,
                                           mime_type   TEXT,
                                           width       INT,
                                           height      INT,
                                           size_bytes  BIGINT DEFAULT 0,
                                           created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_media_room_type ON media_index(room_id, media_type, created_at DESC) WHERE room_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_media_channel_type ON media_index(channel_id, media_type, created_at DESC) WHERE channel_id IS NOT NULL;

-- ===================== POLLS =====================
CREATE TABLE IF NOT EXISTS polls (
                                     id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                     message_id      BIGINT NOT NULL UNIQUE,
                                     room_id         UUID,
                                     channel_id      UUID,
                                     creator_id      UUID NOT NULL REFERENCES users(id),
                                     question        TEXT NOT NULL,
                                     poll_type       SMALLINT DEFAULT 1,
                                     is_anonymous    BOOLEAN DEFAULT true,
                                     allows_multiple BOOLEAN DEFAULT false,
                                     correct_option  INT,
                                     explanation     TEXT,
                                     close_date      TIMESTAMPTZ,
                                     is_closed       BOOLEAN DEFAULT false,
                                     total_voters    INT DEFAULT 0,
                                     created_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS poll_options (
                                            poll_id   UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
                                            option_id INT NOT NULL,
                                            text      VARCHAR(100) NOT NULL,
                                            vote_count INT DEFAULT 0,
                                            PRIMARY KEY (poll_id, option_id)
);

CREATE TABLE IF NOT EXISTS poll_votes (
                                          poll_id   UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
                                          user_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                          option_id INT NOT NULL,
                                          voted_at  TIMESTAMPTZ DEFAULT NOW(),
                                          PRIMARY KEY (poll_id, user_id, option_id)
);
CREATE INDEX IF NOT EXISTS idx_poll_votes_poll ON poll_votes(poll_id);

-- ===================== SLOW MODE =====================
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS slow_mode_interval INT DEFAULT 0;

-- ===================== DRAFTS =====================
CREATE TABLE IF NOT EXISTS drafts (
                                      user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                      room_id             UUID,
                                      channel_id          UUID,
                                      content             TEXT NOT NULL DEFAULT '',
                                      reply_to_message_id BIGINT,
                                      updated_at          TIMESTAMPTZ DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_drafts_user_room_channel
    ON drafts (
               user_id,
               COALESCE(room_id, '00000000-0000-0000-0000-000000000000'::uuid),
               COALESCE(channel_id, '00000000-0000-0000-0000-000000000000'::uuid)
        );

-- ===================== NOTIFICATION SETTINGS =====================
CREATE TABLE IF NOT EXISTS notification_overrides (
                                                      user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                      room_id           UUID,
                                                      channel_id        UUID,
                                                      override_level    TEXT DEFAULT 'default'
                                                          CHECK (override_level IN ('all','mentions','nothing','default')),
                                                      mute_until        TIMESTAMPTZ,
                                                      suppress_everyone BOOLEAN DEFAULT false,
                                                      created_at        TIMESTAMPTZ DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_overrides_user_target
    ON notification_overrides (
                               user_id,
                               COALESCE(room_id, '00000000-0000-0000-0000-000000000000'::uuid),
                               COALESCE(channel_id, '00000000-0000-0000-0000-000000000000'::uuid)
        );

-- ===================== LINK PREVIEW CACHE =====================
CREATE TABLE IF NOT EXISTS link_preview_cache (
                                                  url_hash    TEXT PRIMARY KEY,
                                                  url         TEXT NOT NULL,
                                                  title       TEXT,
                                                  description TEXT,
                                                  image_url   TEXT,
                                                  site_name   TEXT,
                                                  favicon_url TEXT,
                                                  video_url   TEXT,
                                                  fetched_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS message_link_previews (
                                                     message_id  BIGINT NOT NULL,
                                                     url_hash    TEXT NOT NULL REFERENCES link_preview_cache(url_hash),
                                                     position    INT DEFAULT 0,
                                                     suppressed  BOOLEAN DEFAULT false,
                                                     PRIMARY KEY (message_id, url_hash)
);

-- ===================== CHANNEL CATEGORIES =====================
CREATE TABLE IF NOT EXISTS room_categories (
                                               id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                               name      VARCHAR(100) NOT NULL,
                                               position  INT NOT NULL DEFAULT 0,
                                               created_by UUID NOT NULL REFERENCES users(id),
                                               created_at TIMESTAMPTZ DEFAULT NOW()
);

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS category_id UUID REFERENCES room_categories(id);
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS position INT DEFAULT 0;
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS archived BOOLEAN DEFAULT false;

-- ===================== ROLES / PERMISSIONS =====================
CREATE TABLE IF NOT EXISTS roles (
                                     id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                     name        VARCHAR(100) NOT NULL,
                                     color       INT DEFAULT 0,
                                     position    INT NOT NULL DEFAULT 0,
                                     permissions BIGINT NOT NULL DEFAULT 0,
                                     is_default  BOOLEAN DEFAULT false,
                                     created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_roles (
                                          user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                          role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
                                          PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS room_permission_overwrites (
                                                          room_id     UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                                          target_id   UUID NOT NULL,
                                                          target_type TEXT NOT NULL CHECK (target_type IN ('role', 'member')),
                                                          allow_bits  BIGINT NOT NULL DEFAULT 0,
                                                          deny_bits   BIGINT NOT NULL DEFAULT 0,
                                                          UNIQUE(room_id, target_id, target_type)
);

-- ===================== STICKER PACKS =====================
CREATE TABLE IF NOT EXISTS sticker_packs (
                                             id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                             name        VARCHAR(64) NOT NULL,
                                             description TEXT DEFAULT '',
                                             creator_id  UUID NOT NULL REFERENCES users(id),
                                             created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS stickers (
                                        id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                        pack_id     UUID NOT NULL REFERENCES sticker_packs(id) ON DELETE CASCADE,
                                        name        VARCHAR(64) NOT NULL,
                                        tags        TEXT DEFAULT '',
                                        format_type SMALLINT NOT NULL DEFAULT 1,
                                        file_url    TEXT NOT NULL,
                                        file_size   INT NOT NULL DEFAULT 0,
                                        width       INT DEFAULT 512,
                                        height      INT DEFAULT 512,
                                        created_at  TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_stickers_pack ON stickers(pack_id);
CREATE INDEX IF NOT EXISTS idx_stickers_tags ON stickers USING gin(to_tsvector('simple', tags));

CREATE TABLE IF NOT EXISTS user_sticker_packs (
                                                  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                                  pack_id UUID NOT NULL REFERENCES sticker_packs(id) ON DELETE CASCADE,
                                                  added_at TIMESTAMPTZ DEFAULT NOW(),
                                                  PRIMARY KEY (user_id, pack_id)
);

-- ===================== SEARCH IMPROVEMENTS =====================
ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_vector tsvector;
CREATE INDEX IF NOT EXISTS idx_messages_fts_vector ON messages USING GIN(search_vector) WHERE deleted_at IS NULL;

CREATE OR REPLACE FUNCTION messages_search_vector_update() RETURNS trigger AS $$
BEGIN
    NEW.search_vector := to_tsvector('simple', COALESCE(NEW.content, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_messages_search_vector ON messages;
CREATE TRIGGER trg_messages_search_vector
    BEFORE INSERT OR UPDATE OF content ON messages
    FOR EACH ROW EXECUTE FUNCTION messages_search_vector_update();

UPDATE messages SET search_vector = to_tsvector('simple', COALESCE(content, '')) WHERE search_vector IS NULL;
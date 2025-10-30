CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
                                     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                     handle TEXT NOT NULL UNIQUE,
                                     display_name TEXT NOT NULL,
                                     avatar_url TEXT,
                                     oauth_provider TEXT,
                                     oauth_subject TEXT,
                                     password_hash TEXT,
                                     created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                     deleted_at TIMESTAMP WITH TIME ZONE,
                                     CONSTRAINT unique_oauth UNIQUE (oauth_provider, oauth_subject)
);

CREATE INDEX IF NOT EXISTS idx_users_handle ON users(handle);
CREATE INDEX IF NOT EXISTS idx_users_oauth ON users(oauth_provider, oauth_subject) WHERE oauth_provider IS NOT NULL;

CREATE TABLE IF NOT EXISTS voice_servers (
                                             id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                             name TEXT NOT NULL,
                                             region TEXT NOT NULL,
                                             addr_udp TEXT NOT NULL,
                                             addr_ctrl TEXT NOT NULL,
                                             status TEXT NOT NULL DEFAULT 'online',
                                             capacity_hint INTEGER DEFAULT 1000,
                                             load_score DOUBLE PRECISION DEFAULT 0.0,
                                             shared_secret TEXT,
                                             jwks_url TEXT,
                                             owner_user_id UUID REFERENCES users(id),
                                             created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                             updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_voice_servers_region_status ON voice_servers(region, status);
CREATE INDEX IF NOT EXISTS idx_voice_servers_owner ON voice_servers(owner_user_id);

CREATE TABLE IF NOT EXISTS rooms (
                                     id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                     name TEXT NOT NULL,
                                     created_by UUID NOT NULL REFERENCES users(id),
                                     voice_server_id UUID REFERENCES voice_servers(id),
                                     region TEXT,
                                     created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                     deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_rooms_created_by ON rooms(created_by);
CREATE INDEX IF NOT EXISTS idx_rooms_voice_server ON rooms(voice_server_id);

DO $$ BEGIN
    CREATE TYPE membership_role AS ENUM ('member', 'moderator', 'admin');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

CREATE TABLE IF NOT EXISTS memberships (
                                           room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                           user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                           role membership_role NOT NULL DEFAULT 'member',
                                           joined_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                           banned_until TIMESTAMP WITH TIME ZONE,
                                           PRIMARY KEY (room_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_memberships_user ON memberships(user_id);
CREATE INDEX IF NOT EXISTS idx_memberships_room_role ON memberships(room_id, role);

CREATE TABLE IF NOT EXISTS messages (
                                        id BIGINT PRIMARY KEY,
                                        room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                        author_id UUID NOT NULL REFERENCES users(id),
                                        content TEXT NOT NULL,
                                        created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                        edited_at TIMESTAMP WITH TIME ZONE,
                                        deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_messages_room_id ON messages(room_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_author ON messages(author_id);
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages USING BRIN(created_at);

CREATE TABLE IF NOT EXISTS refresh_tokens (
                                              token_hash TEXT PRIMARY KEY,
                                              user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                              expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
                                              created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                              revoked_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires ON refresh_tokens(expires_at);

CREATE TABLE IF NOT EXISTS room_bans (
                                         room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                         user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                         banned_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                         expires_at TIMESTAMP,
                                         created_at TIMESTAMP NOT NULL DEFAULT NOW(),
                                         PRIMARY KEY (room_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_room_bans_expires ON room_bans(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS room_mutes (
                                          room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                          user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                          muted_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                          created_at TIMESTAMP NOT NULL DEFAULT NOW(),
                                          PRIMARY KEY (room_id, user_id)
);
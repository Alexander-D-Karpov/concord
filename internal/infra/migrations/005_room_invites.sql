CREATE TABLE IF NOT EXISTS room_invites (
                                            id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                            room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
                                            invited_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                            invited_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                            status TEXT NOT NULL DEFAULT 'pending',
                                            created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                            updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                            UNIQUE (room_id, invited_user_id),
                                            CHECK (invited_user_id != invited_by),
                                            CHECK (status IN ('pending', 'accepted', 'rejected'))
);

CREATE INDEX IF NOT EXISTS idx_room_invites_invited_user ON room_invites(invited_user_id);
CREATE INDEX IF NOT EXISTS idx_room_invites_invited_by ON room_invites(invited_by);
CREATE INDEX IF NOT EXISTS idx_room_invites_room ON room_invites(room_id);
CREATE INDEX IF NOT EXISTS idx_room_invites_status ON room_invites(status);
CREATE INDEX IF NOT EXISTS idx_room_invites_invited_user_status ON room_invites(invited_user_id, status);
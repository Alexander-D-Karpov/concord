CREATE TABLE IF NOT EXISTS friend_requests (
                                               id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                                               from_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                               to_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                               status TEXT NOT NULL DEFAULT 'pending',
                                               created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                               updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                               UNIQUE (from_user_id, to_user_id),
                                               CHECK (from_user_id != to_user_id),
                                               CHECK (status IN ('pending', 'accepted', 'rejected'))
);

CREATE INDEX IF NOT EXISTS idx_friend_requests_from ON friend_requests(from_user_id);
CREATE INDEX IF NOT EXISTS idx_friend_requests_to ON friend_requests(to_user_id);
CREATE INDEX IF NOT EXISTS idx_friend_requests_status ON friend_requests(status);

CREATE TABLE IF NOT EXISTS friendships (
                                           user_id1 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                           user_id2 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                           created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                           PRIMARY KEY (user_id1, user_id2),
                                           CHECK (user_id1 < user_id2)
);

CREATE INDEX IF NOT EXISTS idx_friendships_user1 ON friendships(user_id1);
CREATE INDEX IF NOT EXISTS idx_friendships_user2 ON friendships(user_id2);

CREATE TABLE IF NOT EXISTS blocked_users (
                                             blocker_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                             blocked_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                             created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                             PRIMARY KEY (blocker_id, blocked_id),
                                             CHECK (blocker_id != blocked_id)
);

CREATE INDEX IF NOT EXISTS idx_blocked_users_blocker ON blocked_users(blocker_id);
CREATE INDEX IF NOT EXISTS idx_blocked_users_blocked ON blocked_users(blocked_id);

CREATE INDEX IF NOT EXISTS idx_friend_requests_to_status ON friend_requests(to_user_id, status);
CREATE INDEX IF NOT EXISTS idx_friend_requests_from_status ON friend_requests(from_user_id, status);
CREATE INDEX IF NOT EXISTS idx_friendships_lookup ON friendships(user_id1, user_id2);

CREATE INDEX IF NOT EXISTS idx_blocked_users_lookup ON blocked_users(blocker_id, blocked_id);

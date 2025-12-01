ALTER TABLE users
    ADD COLUMN IF NOT EXISTS status TEXT DEFAULT 'offline';

CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);

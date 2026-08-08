-- Audit log for moderation and administrative actions.
-- IDs are stored as TEXT (not FKs) so audit history survives room/user deletion.
CREATE TABLE IF NOT EXISTS audit_log (
    id           UUID PRIMARY KEY,
    room_id      TEXT,
    actor_id     TEXT NOT NULL,
    action       TEXT NOT NULL,
    target_id    TEXT,
    target_type  TEXT,
    ip_address   TEXT,
    user_agent   TEXT,
    metadata     JSONB,
    created_at   TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_room_created ON audit_log(room_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log(actor_id);

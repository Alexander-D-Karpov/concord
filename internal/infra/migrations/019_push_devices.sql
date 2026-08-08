-- Registered push (FCM) devices. Keyed by (user_id, device_id) so a client's token
-- rotation is an upsert and logout targets one device. fcm_token is indexed for
-- self-healing pruning by token.
CREATE TABLE IF NOT EXISTS push_devices (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id   TEXT NOT NULL,
    platform    TEXT NOT NULL DEFAULT 'android',
    fcm_token   TEXT NOT NULL,
    app_version TEXT NOT NULL DEFAULT '',
    locale      TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, device_id)
);

CREATE INDEX IF NOT EXISTS idx_push_devices_user ON push_devices(user_id);
CREATE INDEX IF NOT EXISTS idx_push_devices_token ON push_devices(fcm_token);

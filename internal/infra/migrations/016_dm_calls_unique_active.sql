CREATE UNIQUE INDEX IF NOT EXISTS idx_dm_calls_one_active_per_channel
    ON dm_calls(channel_id) WHERE ended_at IS NULL;
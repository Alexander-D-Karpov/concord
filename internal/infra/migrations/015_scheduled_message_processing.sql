ALTER TABLE scheduled_messages ADD COLUMN IF NOT EXISTS attempt_count INT NOT NULL DEFAULT 0;
ALTER TABLE scheduled_messages ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE scheduled_messages ADD COLUMN IF NOT EXISTS processing_started_at TIMESTAMPTZ;
ALTER TABLE scheduled_messages ADD COLUMN IF NOT EXISTS sent_message_id BIGINT;

ALTER TABLE scheduled_messages DROP CONSTRAINT IF EXISTS scheduled_messages_status_check;
ALTER TABLE scheduled_messages ADD CONSTRAINT scheduled_messages_status_check
    CHECK (status IN ('pending','processing','sent','failed','cancelled'));

CREATE INDEX IF NOT EXISTS idx_scheduled_processing
    ON scheduled_messages(processing_started_at) WHERE status = 'processing';
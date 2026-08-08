ALTER TABLE voice_servers ADD COLUMN IF NOT EXISTS secret_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_voice_servers_secret_hash
    ON voice_servers(secret_hash) WHERE secret_hash IS NOT NULL;

UPDATE voice_servers
SET secret_hash = encode(digest(shared_secret, 'sha256'), 'hex')
WHERE shared_secret IS NOT NULL AND secret_hash IS NULL;

ALTER TABLE voice_servers DROP COLUMN IF EXISTS shared_secret;
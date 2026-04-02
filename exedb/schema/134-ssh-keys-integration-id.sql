ALTER TABLE ssh_keys ADD COLUMN integration_id TEXT REFERENCES integrations(integration_id) ON DELETE SET NULL;
CREATE INDEX idx_ssh_keys_integration_id ON ssh_keys(integration_id);

-- Stores the first 4 characters of the exe1 token body for API-generated keys.
-- NULL for non-API keys. Used for UI display only.
ALTER TABLE ssh_keys ADD COLUMN api_key_hint TEXT;

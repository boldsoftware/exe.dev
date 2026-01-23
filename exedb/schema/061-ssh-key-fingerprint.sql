-- Add fingerprint column to ssh_keys table for efficient lookup
-- Fingerprint is the SHA256 hash of the public key, base64 encoded (without "SHA256:" prefix)
-- Empty string means fingerprint not yet computed (will be backfilled by migration 062)
ALTER TABLE ssh_keys ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys(fingerprint);

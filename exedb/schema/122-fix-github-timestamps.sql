-- Fix GitHub account timestamps that were stored with Go's time.String() format
-- (including monotonic clock readings like "m=+29529.384844575").
-- Normalize them to SQLite-compatible "YYYY-MM-DD HH:MM:SS" format.
UPDATE github_accounts
SET access_token_expires_at = parse_timestamp(access_token_expires_at)
WHERE access_token_expires_at IS NOT NULL
  AND access_token_expires_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]*-*'
  AND access_token_expires_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]';

UPDATE github_accounts
SET refresh_token_expires_at = parse_timestamp(refresh_token_expires_at)
WHERE refresh_token_expires_at IS NOT NULL
  AND refresh_token_expires_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]*-*'
  AND refresh_token_expires_at NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]';

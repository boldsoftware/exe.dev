ALTER TABLE github_accounts ADD COLUMN token_renewed_at DATETIME;
ALTER TABLE github_accounts ADD COLUMN access_token_expires_at DATETIME;
ALTER TABLE github_accounts ADD COLUMN refresh_token_expires_at DATETIME;

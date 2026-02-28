-- App tokens for iOS/native app authentication.
-- These are distinct from auth_cookies (browser sessions) and auth_tokens (one-time machine tokens).
-- App tokens are long-lived bearer tokens returned via the app_token auth flow.
CREATE TABLE app_tokens (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_app_tokens_user ON app_tokens(user_id);
CREATE INDEX idx_app_tokens_expires ON app_tokens(expires_at);

-- Thread app token flow state through email verifications and oauth states.
ALTER TABLE email_verifications ADD COLUMN response_mode TEXT;
ALTER TABLE email_verifications ADD COLUMN callback_uri TEXT;
ALTER TABLE oauth_states ADD COLUMN response_mode TEXT;
ALTER TABLE oauth_states ADD COLUMN callback_uri TEXT;

-- Add auth_provider and auth_provider_id columns to users table.
-- auth_provider: NULL = email/passkey (current flow), 'google' = Google OAuth required.
-- auth_provider_id: provider-specific user ID (e.g. Google GAIA ID).
ALTER TABLE users ADD COLUMN auth_provider TEXT;
ALTER TABLE users ADD COLUMN auth_provider_id TEXT;

-- Add auth_provider to pending_team_invites so invites inherit the team's auth method.
ALTER TABLE pending_team_invites ADD COLUMN auth_provider TEXT;

-- OAuth state for CSRF protection during OAuth flows.
CREATE TABLE oauth_states (
    state TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    email TEXT NOT NULL,
    user_id TEXT,
    is_new_user INTEGER NOT NULL DEFAULT 0,
    invite_code_id INTEGER,
    team_invite_token TEXT,
    redirect_url TEXT,
    return_host TEXT,
    login_with_exe INTEGER NOT NULL DEFAULT 0,
    ssh_verification_token TEXT,
    hostname TEXT,
    prompt TEXT,
    image TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);
CREATE INDEX idx_oauth_states_expires ON oauth_states(expires_at);

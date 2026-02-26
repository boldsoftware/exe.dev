-- Team SSO providers for Generic OIDC (Okta, Azure AD, etc.)
-- Each team can have one SSO provider configured.
CREATE TABLE team_sso_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id TEXT NOT NULL UNIQUE REFERENCES teams(team_id),
    provider_type TEXT NOT NULL DEFAULT 'oidc' CHECK (provider_type IN ('oidc')),
    issuer_url TEXT NOT NULL,
    client_id TEXT NOT NULL,
    client_secret TEXT NOT NULL,
    display_name TEXT,
    auth_url TEXT,
    token_url TEXT,
    userinfo_url TEXT,
    last_discovery_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_team_sso_providers_issuer ON team_sso_providers(issuer_url);

-- Add sso_provider_id to oauth_states so the OIDC callback knows which provider to use.
ALTER TABLE oauth_states ADD COLUMN sso_provider_id INTEGER;

-- Team-level auth provider: NULL = email/passkey, 'google' = global Google OAuth, 'oidc' = use team_sso_providers.
ALTER TABLE teams ADD COLUMN auth_provider TEXT;

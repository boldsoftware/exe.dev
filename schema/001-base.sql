-- exe.dev database schema
-- This file is embedded in the Go binary and executed on startup

-- Users table: individual users identified by string user_id with usr prefix
CREATE TABLE IF NOT EXISTS users (
    user_id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Teams table: team names are unique primary keys
-- Teams can be either personal teams (for a single user) or shared teams
-- Personal teams have is_personal=TRUE and cannot have additional members
CREATE TABLE IF NOT EXISTS teams (
    team_name TEXT PRIMARY KEY,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    stripe_customer_id TEXT,
    billing_email TEXT,
    is_personal BOOLEAN DEFAULT FALSE, -- TRUE for personal teams that cannot be shared
    owner_user_id TEXT, -- For personal teams, the owner's user_id
    FOREIGN KEY (owner_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Team membership: links users to teams with admin privileges
CREATE TABLE IF NOT EXISTS team_members (
    user_id TEXT NOT NULL,
    team_name TEXT NOT NULL,
    is_admin BOOLEAN NOT NULL DEFAULT FALSE,
    joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, team_name),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    FOREIGN KEY (team_name) REFERENCES teams(team_name) ON DELETE CASCADE
);

-- Invitations: allow users to join teams via invite codes
-- When inviting by email:
--   - If user exists: just send the invite code
--   - If user doesn't exist: send a link that sets a cookie and directs to signup
-- After signup, users with a valid invite cookie are prompted to join the team
CREATE TABLE IF NOT EXISTS invites (
    code TEXT PRIMARY KEY,
    team_name TEXT NOT NULL,
    created_by_user_id TEXT NOT NULL,
    email TEXT, -- optional: invite specific email
    max_uses INTEGER DEFAULT 1,
    used_count INTEGER DEFAULT 0,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (team_name) REFERENCES teams(team_name) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Machines: containers/VMs with global unique IDs
CREATE TABLE IF NOT EXISTS machines (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team_name TEXT NOT NULL,
    name TEXT NOT NULL, -- name within the team (for <name>.<team>.exe.dev)
    status TEXT NOT NULL DEFAULT 'stopped', -- stopped, starting, running, stopping
    image TEXT,
    container_id TEXT, -- Docker container ID for this machine
    created_by_user_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    -- SSH key material for container access
    ssh_server_identity_key TEXT, -- SSH server private key (PEM)
    ssh_authorized_keys TEXT,     -- User certificate for authorized_keys
    ssh_ca_public_key TEXT,       -- CA public key for mutual auth
    ssh_host_certificate TEXT,    -- Host certificate for host key validation
    ssh_client_private_key TEXT,  -- Private key for connecting to container
    ssh_port INTEGER,  -- SSH port exposed for this container (as seen on docker host)
    docker_host TEXT,              -- DOCKER_HOST value where this container runs
    routes TEXT, -- JSON-encoded routing configuration (from migration 003)
    UNIQUE(team_name, name), -- name must be unique within team
    FOREIGN KEY (team_name) REFERENCES teams(team_name) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id)
);

-- DNS aliases: additional DNS names for machines (beyond <name>.<team>.exe.dev)
CREATE TABLE IF NOT EXISTS dns_aliases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id INTEGER NOT NULL,
    hostname TEXT UNIQUE NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (machine_id) REFERENCES machines(id) ON DELETE CASCADE
);

-- Email verification: temporary tokens for email verification
CREATE TABLE IF NOT EXISTS email_verifications (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Billing verification: temporary state for billing setup
CREATE TABLE IF NOT EXISTS billing_verifications (
    user_id TEXT PRIMARY KEY,
    team_name TEXT NOT NULL,
    stripe_payment_method TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    FOREIGN KEY (team_name) REFERENCES teams(team_name) ON DELETE CASCADE
);

-- Auth cookies: HTTP authentication cookies for web access
CREATE TABLE IF NOT EXISTS auth_cookies (
    cookie_value TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    domain TEXT NOT NULL, -- exe.dev or localhost
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Auth tokens: temporary tokens for magic link authentication
CREATE TABLE IF NOT EXISTS auth_tokens (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    subdomain TEXT, -- container.team for subdomain access (optional)
    expires_at DATETIME NOT NULL,
    used_at DATETIME, -- NULL if not used yet
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- SSH keys table: supports multiple SSH keys per user
-- Each SSH key has a default team that is used when no team is specified
CREATE TABLE IF NOT EXISTS ssh_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    public_key TEXT UNIQUE NOT NULL, -- Public keys are globally unique to identify users
    device_name TEXT, -- Optional: friendly name for the key
    default_team TEXT, -- Default team for this SSH key
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    verified BOOLEAN DEFAULT FALSE, -- Whether this key has been verified via email
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    FOREIGN KEY (default_team) REFERENCES teams(team_name) ON DELETE SET NULL
);

-- Table for pending SSH key additions (when logging in from new device)
CREATE TABLE IF NOT EXISTS pending_ssh_keys (
    token TEXT PRIMARY KEY,
    public_key TEXT NOT NULL,
    user_email TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Table for pending registrations with team name reservation
CREATE TABLE IF NOT EXISTS pending_registrations (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    team_name TEXT UNIQUE NOT NULL, -- Reserved team name
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- SSH host key storage: ensures consistent host key across restarts
CREATE TABLE IF NOT EXISTS ssh_host_key (
    id INTEGER PRIMARY KEY CHECK (id = 1), -- Ensure only one row
    private_key TEXT NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Migrations table to track which migrations have been executed
CREATE TABLE IF NOT EXISTS migrations (
    migration_number INTEGER PRIMARY KEY,
    migration_name TEXT NOT NULL,
    executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Insert migration entries into the table (combined from all previous migrations)
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (001, '001_base');

-- Indexes for common queries
-- Index removed: users no longer have public_key_fingerprint column
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_team_members_team ON team_members(team_name);
CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_id);
CREATE INDEX IF NOT EXISTS idx_machines_team ON machines(team_name);
CREATE INDEX IF NOT EXISTS idx_machines_status ON machines(status);
CREATE INDEX IF NOT EXISTS idx_invites_team ON invites(team_name);
CREATE INDEX IF NOT EXISTS idx_invites_expires ON invites(expires_at);
CREATE INDEX IF NOT EXISTS idx_email_verifications_expires ON email_verifications(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_cookies_user ON auth_cookies(user_id);
CREATE INDEX IF NOT EXISTS idx_auth_cookies_expires ON auth_cookies(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_expires ON auth_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_subdomain ON auth_tokens(subdomain);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_user_id ON ssh_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_public_key ON ssh_keys(public_key);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_default_team ON ssh_keys(default_team);
CREATE INDEX IF NOT EXISTS idx_pending_ssh_keys_expires ON pending_ssh_keys(expires_at);
CREATE INDEX IF NOT EXISTS idx_pending_registrations_expires ON pending_registrations(expires_at);
CREATE INDEX IF NOT EXISTS idx_pending_registrations_team ON pending_registrations(team_name);
CREATE INDEX IF NOT EXISTS idx_teams_personal ON teams(is_personal);
CREATE INDEX IF NOT EXISTS idx_teams_owner ON teams(owner_user_id);

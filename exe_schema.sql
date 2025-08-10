-- exe.dev database schema
-- This file is embedded in the Go binary and executed on startup

-- Teams table: team names are unique primary keys
CREATE TABLE IF NOT EXISTS teams (
    name TEXT PRIMARY KEY,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    stripe_customer_id TEXT,
    billing_email TEXT
);

-- Users table: individual users identified by SSH key fingerprint
CREATE TABLE IF NOT EXISTS users (
    public_key_fingerprint TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Team membership: links users to teams with admin privileges
CREATE TABLE IF NOT EXISTS team_members (
    user_fingerprint TEXT NOT NULL,
    team_name TEXT NOT NULL,
    is_admin BOOLEAN NOT NULL DEFAULT FALSE,
    joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_fingerprint, team_name),
    FOREIGN KEY (user_fingerprint) REFERENCES users(public_key_fingerprint) ON DELETE CASCADE,
    FOREIGN KEY (team_name) REFERENCES teams(name) ON DELETE CASCADE
);

-- Invitations: allow users to join teams via invite codes
CREATE TABLE IF NOT EXISTS invites (
    code TEXT PRIMARY KEY,
    team_name TEXT NOT NULL,
    created_by_fingerprint TEXT NOT NULL,
    email TEXT, -- optional: invite specific email
    max_uses INTEGER DEFAULT 1,
    used_count INTEGER DEFAULT 0,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (team_name) REFERENCES teams(name) ON DELETE CASCADE,
    FOREIGN KEY (created_by_fingerprint) REFERENCES users(public_key_fingerprint) ON DELETE CASCADE
);

-- Machines: containers/VMs with global unique IDs
CREATE TABLE IF NOT EXISTS machines (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team_name TEXT NOT NULL,
    name TEXT NOT NULL, -- name within the team (for <name>.<team>.exe.dev)
    status TEXT NOT NULL DEFAULT 'stopped', -- stopped, starting, running, stopping
    image TEXT,
    container_id TEXT, -- GKE container/pod ID for this machine
    created_by_fingerprint TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    UNIQUE(team_name, name), -- name must be unique within team
    FOREIGN KEY (team_name) REFERENCES teams(name) ON DELETE CASCADE,
    FOREIGN KEY (created_by_fingerprint) REFERENCES users(public_key_fingerprint)
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
    user_fingerprint TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_fingerprint) REFERENCES users(public_key_fingerprint) ON DELETE CASCADE
);

-- Billing verification: temporary state for billing setup
CREATE TABLE IF NOT EXISTS billing_verifications (
    user_fingerprint TEXT PRIMARY KEY,
    team_name TEXT NOT NULL,
    stripe_payment_method TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_fingerprint) REFERENCES users(public_key_fingerprint) ON DELETE CASCADE,
    FOREIGN KEY (team_name) REFERENCES teams(name) ON DELETE CASCADE
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_team_members_team ON team_members(team_name);
CREATE INDEX IF NOT EXISTS idx_team_members_user ON team_members(user_fingerprint);
CREATE INDEX IF NOT EXISTS idx_machines_team ON machines(team_name);
CREATE INDEX IF NOT EXISTS idx_machines_status ON machines(status);
CREATE INDEX IF NOT EXISTS idx_invites_team ON invites(team_name);
CREATE INDEX IF NOT EXISTS idx_invites_expires ON invites(expires_at);
CREATE INDEX IF NOT EXISTS idx_email_verifications_expires ON email_verifications(expires_at);
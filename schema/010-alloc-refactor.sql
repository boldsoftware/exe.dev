-- Migration 010: Replace teams with allocs
-- This migration removes the concept of teams and replaces it with allocs (resource allocations)

-- Create new allocs table
CREATE TABLE IF NOT EXISTS allocs (
    alloc_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    alloc_type TEXT NOT NULL DEFAULT 'medium', -- 'medium' is the only type for now
    region TEXT NOT NULL DEFAULT 'aws-us-west-2', -- only region for now
    docker_host TEXT, -- Docker host where this alloc's containers run
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    stripe_customer_id TEXT,
    billing_email TEXT,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Update machines table to use alloc_id instead of team_name
-- Since we're not worrying about backward compatibility, we'll recreate the table
DROP TABLE IF EXISTS machines;
CREATE TABLE machines (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alloc_id TEXT NOT NULL,
    name TEXT NOT NULL, -- name for <name>.exe.dev
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
    routes TEXT, -- JSON-encoded routing configuration
    UNIQUE(name), -- machine names are globally unique now
    FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id)
);

-- Drop team-related tables
DROP TABLE IF EXISTS dns_aliases; -- We'll handle this differently if needed
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS billing_verifications;
DROP TABLE IF EXISTS pending_registrations;

-- Update SSH keys table to remove default_team
DROP TABLE IF EXISTS ssh_keys;
CREATE TABLE ssh_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    public_key TEXT UNIQUE NOT NULL, -- Public keys are globally unique to identify users
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    verified BOOLEAN DEFAULT FALSE, -- Whether this key has been verified via email
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Update auth_tokens table to remove subdomain concept (was container.team)
DROP TABLE IF EXISTS auth_tokens;
CREATE TABLE auth_tokens (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    machine_name TEXT, -- Direct machine name for access (optional)
    expires_at DATETIME NOT NULL,
    used_at DATETIME, -- NULL if not used yet
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Create indexes for new structure
CREATE INDEX IF NOT EXISTS idx_allocs_user ON allocs(user_id);
CREATE INDEX IF NOT EXISTS idx_allocs_region ON allocs(region);
CREATE INDEX IF NOT EXISTS idx_allocs_docker_host ON allocs(docker_host);
CREATE INDEX IF NOT EXISTS idx_machines_alloc ON machines(alloc_id);
CREATE INDEX IF NOT EXISTS idx_machines_name ON machines(name);
CREATE INDEX IF NOT EXISTS idx_machines_status ON machines(status);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_expires ON auth_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_machine ON auth_tokens(machine_name);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_user_id ON ssh_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_public_key ON ssh_keys(public_key);

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (010, '010_alloc_refactor');
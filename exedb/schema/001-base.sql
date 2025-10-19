CREATE TABLE ssh_host_key ( id INTEGER PRIMARY KEY CHECK (id = 1), private_key TEXT NOT NULL, public_key TEXT NOT NULL, fingerprint TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE users (
    user_id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
, default_billing_account_id TEXT NOT NULL REFERENCES billing_accounts(billing_account_id));
CREATE TABLE email_verifications (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, verification_code TEXT,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE auth_cookies (
    cookie_value TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    domain TEXT NOT NULL, -- exe.dev or localhost
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE pending_ssh_keys (
    token TEXT PRIMARY KEY,
    public_key TEXT NOT NULL,
    user_email TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE migrations (
    migration_number INTEGER PRIMARY KEY,
    migration_name TEXT NOT NULL,
    executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_email_verifications_expires ON email_verifications(expires_at);
CREATE INDEX idx_auth_cookies_user ON auth_cookies(user_id);
CREATE INDEX idx_auth_cookies_expires ON auth_cookies(expires_at);
CREATE INDEX idx_pending_ssh_keys_expires ON pending_ssh_keys(expires_at);
CREATE TABLE ssh_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    public_key TEXT UNIQUE NOT NULL, -- Public keys are globally unique to identify users
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE auth_tokens (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    machine_name TEXT, -- Direct machine name for access (optional)
    expires_at DATETIME NOT NULL,
    used_at DATETIME, -- NULL if not used yet
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_auth_tokens_expires ON auth_tokens(expires_at);
CREATE INDEX idx_auth_tokens_machine ON auth_tokens(machine_name);
CREATE INDEX idx_ssh_keys_user_id ON ssh_keys(user_id);
CREATE INDEX idx_ssh_keys_public_key ON ssh_keys(public_key);
CREATE TABLE tag_resolutions (
    -- Primary key components
    registry TEXT NOT NULL,      -- e.g., 'docker.io', 'ghcr.io', 'quay.io'
    repository TEXT NOT NULL,     -- e.g., 'library/ubuntu', 'boldsoftware/exeuntu'
    tag TEXT NOT NULL,           -- e.g., 'latest', '22.04', 'v1.0.0'
    
    -- Digest information
    index_digest TEXT,           -- Manifest list/OCI index digest (sha256:...)
    platform_digest TEXT,        -- Platform-specific manifest digest (sha256:...)
    platform TEXT NOT NULL DEFAULT 'linux/amd64', -- Platform identifier
    
    -- Timing information
    last_checked_at INTEGER NOT NULL,  -- Unix timestamp of last upstream check
    last_changed_at INTEGER NOT NULL,  -- Unix timestamp of last digest change
    ttl_seconds INTEGER NOT NULL DEFAULT 21600, -- 6 hours default TTL
    
    -- Tracking information
    seen_on_hosts INTEGER DEFAULT 0,   -- Counter of hosts that have this image
    image_size INTEGER,                 -- Image size in bytes (for progress reporting)
    
    -- Metadata
    created_at INTEGER NOT NULL,       -- Unix timestamp when record was created
    updated_at INTEGER NOT NULL, image_user TEXT, image_entrypoint TEXT, image_cmd TEXT, image_login_user TEXT, image_labels TEXT, image_exposed_ports TEXT,       -- Unix timestamp when record was last updated
    
    PRIMARY KEY (registry, repository, tag, platform)
);
CREATE INDEX idx_tag_resolutions_check_time 
    ON tag_resolutions(last_checked_at, ttl_seconds);
CREATE INDEX idx_tag_resolutions_digest 
    ON tag_resolutions(platform_digest) WHERE platform_digest IS NOT NULL;
CREATE TABLE tag_resolution_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    registry TEXT NOT NULL,
    repository TEXT NOT NULL,
    tag TEXT NOT NULL,
    platform TEXT NOT NULL,
    old_digest TEXT,
    new_digest TEXT NOT NULL,
    changed_at INTEGER NOT NULL,  -- Unix timestamp
    
    FOREIGN KEY (registry, repository, tag, platform) 
        REFERENCES tag_resolutions(registry, repository, tag, platform)
);
CREATE INDEX idx_tag_resolution_history_time 
    ON tag_resolution_history(changed_at DESC);
CREATE TABLE deleted_boxes (
    id INTEGER PRIMARY KEY,  -- Same as the original box id
    alloc_id TEXT NOT NULL,
    deleted_at TEXT DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_deleted_boxes_alloc_id ON deleted_boxes(alloc_id);
CREATE INDEX idx_deleted_boxes_deleted_at ON deleted_boxes(deleted_at);
CREATE TABLE IF NOT EXISTS "allocs" (
    alloc_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    alloc_type TEXT NOT NULL DEFAULT 'medium',
    region TEXT NOT NULL DEFAULT 'aws-us-west-2',
    ctrhost TEXT NOT NULL,  -- Container host where this alloc's resources are
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    billing_account_id TEXT NOT NULL REFERENCES billing_accounts(billing_account_id),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_allocs_user ON allocs(user_id);
CREATE INDEX idx_allocs_region ON allocs(region);
CREATE INDEX idx_allocs_ctrhost ON allocs(ctrhost);
CREATE INDEX idx_tag_resolutions_host_lookup 
    ON tag_resolutions(registry, repository, tag, platform);
CREATE TABLE server_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE user_events (
    user_id TEXT NOT NULL,
    event TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    first_occurred_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_occurred_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, event),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS "boxes" (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alloc_id TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    image TEXT NOT NULL,
    container_id TEXT,
    created_by_user_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    routes TEXT,
    ssh_server_identity_key BLOB,
    ssh_authorized_keys TEXT,
    -- ssh_ca_public_key TEXT removed
    -- ssh_host_certificate TEXT removed  
    ssh_client_private_key BLOB,
    ssh_port INTEGER,
    ssh_user TEXT, creation_log TEXT,
    UNIQUE(name),
    FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_boxes_alloc_id ON boxes(alloc_id);
CREATE INDEX idx_boxes_status ON boxes(status);
CREATE TABLE billing_accounts (
    billing_account_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    billing_email TEXT,
    stripe_customer_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_billing_accounts_stripe_customer ON billing_accounts(stripe_customer_id);
CREATE INDEX idx_allocs_billing_account ON allocs(billing_account_id);
CREATE INDEX idx_users_default_billing_account ON users(default_billing_account_id);
CREATE TABLE usage_credits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    billing_account_id TEXT NOT NULL,
    amount REAL NOT NULL,
    payment_method TEXT NOT NULL,
    payment_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    data TEXT, -- JSON for additional payment-specific data
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (billing_account_id) REFERENCES billing_accounts(billing_account_id) ON DELETE CASCADE
);
CREATE INDEX idx_usage_credits_billing_account ON usage_credits(billing_account_id);
CREATE TABLE usage_debits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    billing_account_id TEXT NOT NULL,
    model TEXT NOT NULL,
    message_id TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (billing_account_id) REFERENCES billing_accounts(billing_account_id) ON DELETE CASCADE
);
CREATE INDEX idx_usage_debits_billing_account ON usage_debits(billing_account_id);
CREATE INDEX idx_usage_debits_message_id ON usage_debits(message_id);
CREATE TABLE waitlist (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    remote_ip TEXT,
    json TEXT, -- JSON blob, e.g. {"meaning": ["Joy", ...]}
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_waitlist_email ON waitlist(email);
CREATE TABLE mobile_pending_vm (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    hostname TEXT NOT NULL,
    prompt TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_mobile_pending_user ON mobile_pending_vm(user_id, created_at);

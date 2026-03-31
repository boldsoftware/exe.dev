-- Base schema snapshot created by consolidate-migrations.
-- Consolidates migrations 001 through 132.
-- This migration is skipped on existing databases (only runs on fresh databases).
-- All schema changes must be done via new migrations.

CREATE TABLE ssh_host_key ( id INTEGER PRIMARY KEY CHECK (id = 1), private_key TEXT NOT NULL, public_key TEXT NOT NULL, fingerprint TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, cert_sig TEXT);
CREATE TABLE users (
    user_id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
, root_support INTEGER NOT NULL DEFAULT 0, created_for_login_with_exe INTEGER NOT NULL DEFAULT 0, new_vm_creation_disabled INTEGER NOT NULL DEFAULT 0, discord_id TEXT, discord_username TEXT, signed_up_with_invite_id INTEGER REFERENCES invite_codes(id), next_ssh_key_number INTEGER NOT NULL DEFAULT 1, region TEXT NOT NULL DEFAULT 'pdx', canonical_email TEXT, is_locked_out INTEGER NOT NULL DEFAULT 0, limits TEXT, cgroup_overrides TEXT, newsletter_subscribed INTEGER NOT NULL DEFAULT 0, auth_provider TEXT, auth_provider_id TEXT);
CREATE TABLE email_verifications (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, verification_code TEXT, invite_code_id INTEGER REFERENCES invite_codes(id), is_new_user INTEGER NOT NULL DEFAULT 0, redirect_url TEXT, return_host TEXT, response_mode TEXT, callback_uri TEXT, verification_code_attempts INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE auth_cookies (
    cookie_value TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    domain TEXT NOT NULL, -- exe.dev or localhost
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP, -- UTC day granularity; subsequent writes use DATE('now'). See UpdateAuthCookieLastUsed.
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE pending_ssh_keys (
    token TEXT PRIMARY KEY,
    public_key TEXT NOT NULL,
    user_email TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_email_verifications_expires ON email_verifications(expires_at);
CREATE INDEX idx_auth_cookies_user ON auth_cookies(user_id);
CREATE INDEX idx_auth_cookies_expires ON auth_cookies(expires_at);
CREATE INDEX idx_pending_ssh_keys_expires ON pending_ssh_keys(expires_at);
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
    updated_at INTEGER NOT NULL,       -- Unix timestamp when record was last updated
    
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
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, vm_image TEXT DEFAULT '',
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_mobile_pending_user ON mobile_pending_vm(user_id, created_at);
CREATE TABLE pending_box_shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    shared_with_email TEXT NOT NULL,
    shared_by_user_id TEXT NOT NULL,
    message TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (shared_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    UNIQUE(box_id, shared_with_email)
);
CREATE INDEX idx_pending_box_shares_box ON pending_box_shares(box_id);
CREATE INDEX idx_pending_box_shares_email ON pending_box_shares(shared_with_email);
CREATE TABLE box_shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    shared_with_user_id TEXT NOT NULL,
    shared_by_user_id TEXT NOT NULL,
    message TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (shared_with_user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    FOREIGN KEY (shared_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    UNIQUE(box_id, shared_with_user_id)
);
CREATE INDEX idx_box_shares_box ON box_shares(box_id);
CREATE INDEX idx_box_shares_user ON box_shares(shared_with_user_id);
CREATE TABLE box_share_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    share_token TEXT NOT NULL UNIQUE,
    created_by_user_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    use_count INTEGER DEFAULT 0,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_box_share_links_box ON box_share_links(box_id);
CREATE INDEX idx_box_share_links_token ON box_share_links(share_token);
CREATE TABLE user_daily_email_counts (
    user_id TEXT NOT NULL,
    date TEXT NOT NULL,
    email_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, date),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_daily_email_counts_user_date ON user_daily_email_counts(user_id, date);
CREATE TABLE boxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    image TEXT NOT NULL,
    ctrhost TEXT NOT NULL,
    container_id TEXT,
    created_by_user_id TEXT NOT NULL, -- <== Existing column now replacing alloc_id
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    routes TEXT,
    ssh_server_identity_key BLOB,
    ssh_authorized_keys TEXT,
    ssh_client_private_key BLOB,
    ssh_port INTEGER,
    ssh_user TEXT,
    creation_log TEXT, support_access_allowed INTEGER NOT NULL DEFAULT 0, region TEXT NOT NULL DEFAULT 'pdx', email_receive_enabled INTEGER NOT NULL DEFAULT 0, email_maildir_path TEXT NOT NULL DEFAULT '', allocated_cpus INTEGER, cgroup_overrides TEXT, tags TEXT NOT NULL DEFAULT '[]', lock_reason TEXT,
    UNIQUE(name),
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE deleted_boxes (
    id INTEGER PRIMARY KEY,
    user_id TEXT NOT NULL,
    deleted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE passkeys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    credential_id BLOB NOT NULL UNIQUE,
    public_key BLOB NOT NULL,
    sign_count INTEGER NOT NULL DEFAULT 0,
    aaguid BLOB,
    name TEXT NOT NULL DEFAULT '',
    flags INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_passkeys_user_id ON passkeys(user_id);
CREATE INDEX idx_passkeys_credential_id ON passkeys(credential_id);
CREATE TABLE passkey_challenges (
    challenge TEXT PRIMARY KEY,
    session_data BLOB NOT NULL,
    user_id TEXT,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_passkey_challenges_expires ON passkey_challenges(expires_at);
CREATE TABLE email_address_quality (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    queried_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    response_json TEXT NOT NULL,
    disposable INTEGER GENERATED ALWAYS AS (json_extract(response_json, '$.disposable')) STORED
);
CREATE INDEX idx_email_address_quality_email ON email_address_quality(email);
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, parent_id TEXT REFERENCES accounts(id), status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'restricted', 'past_due')),
    FOREIGN KEY (created_by) REFERENCES users(user_id)
);
CREATE TABLE hll_sketches (
    key TEXT PRIMARY KEY,
    data BLOB NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE email_bounces (
    email TEXT PRIMARY KEY NOT NULL,
    reason TEXT NOT NULL,
    bounced_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE signup_rejections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    ip TEXT NOT NULL,
    reason TEXT NOT NULL,
    source TEXT NOT NULL,
    rejected_at DATETIME DEFAULT CURRENT_TIMESTAMP
, ipqs_response_json TEXT);
CREATE INDEX idx_signup_rejections_email ON signup_rejections(email);
CREATE INDEX idx_signup_rejections_ip ON signup_rejections(ip);
CREATE INDEX idx_signup_rejections_rejected_at ON signup_rejections(rejected_at);
CREATE TABLE email_quality_bypass (
    email TEXT PRIMARY KEY NOT NULL,
    reason TEXT NOT NULL,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    added_by TEXT NOT NULL
);
CREATE TABLE shell_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    command TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_shell_history_user ON shell_history(user_id);
CREATE INDEX idx_users_discord_id ON users(discord_id);
CREATE TABLE invite_code_pool (
    code TEXT PRIMARY KEY,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE invite_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL UNIQUE,
    plan_type TEXT NOT NULL CHECK (plan_type IN ('trial', 'free')),
    -- null for system codes, otherwise the user who can share this code
    assigned_to_user_id TEXT,
    assigned_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    -- who created this invite code (admin email or tailscale identity)
    assigned_by TEXT NOT NULL,
    -- optional notes about who this code is intended for
    assigned_for TEXT,
    -- populated when the code is used by a new user
    used_by_user_id TEXT,
    used_at DATETIME, allocated_at TIMESTAMP, is_batch INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (assigned_to_user_id) REFERENCES users(user_id) ON DELETE SET NULL,
    FOREIGN KEY (used_by_user_id) REFERENCES users(user_id) ON DELETE SET NULL
);
CREATE INDEX idx_invite_codes_code ON invite_codes(code);
CREATE INDEX idx_invite_codes_assigned_to ON invite_codes(assigned_to_user_id) WHERE assigned_to_user_id IS NOT NULL;
CREATE INDEX idx_invite_codes_unused ON invite_codes(id) WHERE used_by_user_id IS NULL;
CREATE TABLE pending_registrations (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    invite_code_id INTEGER REFERENCES invite_codes(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
, account_id TEXT);
CREATE INDEX idx_pending_registrations_email ON pending_registrations(email);
CREATE INDEX idx_pending_registrations_expires ON pending_registrations(expires_at);
CREATE TABLE user_llm_credit (
    user_id TEXT PRIMARY KEY REFERENCES users(user_id),
    available_credit REAL NOT NULL DEFAULT 100.0,
    max_credit REAL,  -- NULL means use default (currently 100.0)
    refresh_per_hour REAL,  -- NULL means use default (currently 10.0)
    total_used REAL NOT NULL DEFAULT 0.0,
    last_refresh_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
, billing_upgrade_bonus_granted INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS "ssh_keys" (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    public_key TEXT UNIQUE NOT NULL,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME, -- UTC day granularity; writes use DATE('now'). See UpdateSSHKeyLastUsed.
    comment TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_ssh_keys_user_id ON ssh_keys(user_id);
CREATE UNIQUE INDEX idx_ssh_keys_public_key ON ssh_keys(public_key);
CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys(fingerprint);
CREATE TABLE billing_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('active', 'canceled')),
    event_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, stripe_event_id TEXT,
    FOREIGN KEY (account_id) REFERENCES accounts(id)
);
CREATE INDEX idx_billing_events_account_event_at ON billing_events(account_id, event_at DESC);
CREATE UNIQUE INDEX idx_billing_events_unique ON billing_events(account_id, event_type, event_at);
CREATE TABLE user_defaults (
    user_id TEXT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    new_vm_email INTEGER, -- NULL = not set (use default), 0 = false, 1 = true
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
, github_integration INTEGER, new_setup_script TEXT);
CREATE TABLE box_email_credit (
    box_id INTEGER PRIMARY KEY REFERENCES boxes(id),
    available_credit REAL NOT NULL DEFAULT 50.0,
    last_refresh_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    total_sent INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_boxes_region ON boxes(region);
CREATE INDEX idx_users_canonical_email ON users(canonical_email);
CREATE TABLE IF NOT EXISTS "billing_credits" (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    amount INTEGER NOT NULL,
    stripe_event_id TEXT UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, hour_bucket DATETIME, credit_type TEXT, note TEXT, gift_id TEXT);
CREATE TABLE checkout_params (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    vm_name TEXT NOT NULL DEFAULT '',
    vm_prompt TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, vm_image TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_checkout_params_created_at ON checkout_params(created_at);
CREATE INDEX idx_billing_credits_account
ON billing_credits(account_id);
CREATE UNIQUE INDEX idx_billing_credits_account_hour_type
ON billing_credits(account_id, hour_bucket, credit_type);
CREATE TABLE pending_team_invites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    email TEXT NOT NULL,
    canonical_email TEXT NOT NULL,
    invited_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    token TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accepted_at DATETIME,
    accepted_by_user_id TEXT, auth_provider TEXT,
    UNIQUE(team_id, canonical_email)
);
CREATE INDEX idx_pending_team_invites_canonical_email ON pending_team_invites(canonical_email);
CREATE INDEX idx_pending_team_invites_token ON pending_team_invites(token);
CREATE TABLE vm_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    short_description TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'other',
    prompt TEXT NOT NULL,
    icon_url TEXT NOT NULL DEFAULT '',
    screenshot_url TEXT NOT NULL DEFAULT '',
    author_user_id TEXT REFERENCES users(user_id),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    featured INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
, vm_shortname TEXT NOT NULL DEFAULT '', image TEXT NOT NULL DEFAULT '', deploy_count INTEGER NOT NULL DEFAULT 0);
CREATE INDEX idx_vm_templates_status ON vm_templates(status);
CREATE INDEX idx_vm_templates_category ON vm_templates(category);
CREATE INDEX idx_vm_templates_author ON vm_templates(author_user_id);
CREATE TABLE template_ratings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id INTEGER NOT NULL REFERENCES vm_templates(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    rating INTEGER NOT NULL CHECK (rating >= 1 AND rating <= 5),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(template_id, user_id)
);
CREATE INDEX idx_template_ratings_template ON template_ratings(template_id);
CREATE UNIQUE INDEX idx_vm_templates_shortname
    ON vm_templates(vm_shortname)
    WHERE vm_shortname != '' AND status = 'approved';
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
, sso_provider_id INTEGER, response_mode TEXT, callback_uri TEXT);
CREATE INDEX idx_oauth_states_expires ON oauth_states(expires_at);
CREATE TABLE redirects (
    key TEXT PRIMARY KEY,
    target TEXT NOT NULL,
    expires_at DATETIME NOT NULL
);
CREATE INDEX idx_redirects_expires ON redirects(expires_at);
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
CREATE TABLE integrations (
    integration_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    type TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    config TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, attachments TEXT NOT NULL DEFAULT '[]',
    FOREIGN KEY (owner_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_integrations_owner ON integrations(owner_user_id);
CREATE UNIQUE INDEX idx_integrations_owner_name ON integrations(owner_user_id, name);
CREATE TABLE IF NOT EXISTS "migrations" (
    migration_name TEXT PRIMARY KEY,
    migration_number INTEGER NOT NULL,
    executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE exe1_tokens (
    exe1 TEXT PRIMARY KEY,
    exe0 TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE signup_ip_checks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    ip TEXT NOT NULL,
    source TEXT NOT NULL,
    ipqs_response_json TEXT,
    flagged INTEGER NOT NULL DEFAULT 0,
    checked_at DATETIME DEFAULT CURRENT_TIMESTAMP
, error TEXT);
CREATE INDEX idx_signup_ip_checks_email ON signup_ip_checks(email);
CREATE INDEX idx_signup_ip_checks_ip ON signup_ip_checks(ip);
CREATE INDEX idx_signup_ip_checks_checked_at ON signup_ip_checks(checked_at);
CREATE TABLE released_box_names (
    name TEXT NOT NULL,
    box_id INTEGER NOT NULL,
    user_id TEXT NOT NULL,
    released_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (name),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_billing_credits_gift_id ON billing_credits(gift_id);
CREATE TABLE IF NOT EXISTS "box_ip_shard" (
    box_id INTEGER NOT NULL,
    user_id TEXT NOT NULL,
    ip_shard INTEGER NOT NULL,
    PRIMARY KEY (box_id, user_id),
    UNIQUE(ip_shard, box_id, user_id),
    CHECK (ip_shard BETWEEN 1 AND 1016),
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_box_ip_shard_user ON box_ip_shard(user_id);
CREATE INDEX idx_box_ip_shard_shard ON box_ip_shard(ip_shard);
CREATE TABLE IF NOT EXISTS "netactuate_ip_shards" (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE push_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    token TEXT NOT NULL,
    platform TEXT NOT NULL DEFAULT 'apns',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME, environment TEXT NOT NULL DEFAULT 'production',
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_push_tokens_token ON push_tokens(token);
CREATE INDEX idx_push_tokens_user ON push_tokens(user_id);
CREATE TABLE account_plans (
    account_id   TEXT     NOT NULL REFERENCES accounts(id),
    plan_id      TEXT     NOT NULL,
    started_at   DATETIME NOT NULL,
    ended_at     DATETIME,
    trial_expires_at DATETIME,
    changed_by   TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_account_plans_account ON account_plans(account_id);
CREATE UNIQUE INDEX idx_account_plans_active
ON account_plans(account_id) WHERE ended_at IS NULL;
CREATE UNIQUE INDEX idx_accounts_created_by ON accounts(created_by);
CREATE TABLE github_user_tokens (
    user_id TEXT NOT NULL,
    github_login TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    token_renewed_at DATETIME,
    access_token_expires_at DATETIME,
    refresh_token_expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, github_login),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE TABLE github_installations (
    user_id TEXT NOT NULL,
    github_login TEXT NOT NULL,
    github_app_installation_id INTEGER NOT NULL,
    github_account_login TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, github_app_installation_id),
    FOREIGN KEY (user_id, github_login) REFERENCES github_user_tokens(user_id, github_login) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_github_installations_user_account
    ON github_installations(user_id, github_account_login);
CREATE INDEX idx_boxes_ctrhost ON boxes(ctrhost);
CREATE INDEX idx_auth_cookies_domain ON auth_cookies(domain);
CREATE INDEX idx_boxes_created_by_user_id ON boxes(created_by_user_id);
CREATE UNIQUE INDEX idx_billing_events_stripe_event_id ON billing_events(stripe_event_id) WHERE stripe_event_id IS NOT NULL;
CREATE TABLE stripe_webhook_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stripe_event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_stripe_webhook_events_type ON stripe_webhook_events(event_type);
CREATE INDEX idx_stripe_webhook_events_created ON stripe_webhook_events(created_at);
CREATE TABLE IF NOT EXISTS "teams" (
    team_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    limits TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    auth_provider TEXT
);
CREATE TABLE IF NOT EXISTS "team_members" (
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    user_id TEXT NOT NULL REFERENCES users(user_id),
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('billing_owner', 'admin', 'user')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, user_id),
    UNIQUE (user_id)
);
CREATE INDEX idx_team_members_team_id ON team_members(team_id);
CREATE TABLE IF NOT EXISTS "box_team_shares" (
    box_id INTEGER NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    shared_by TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (box_id, team_id)
);

-- Pre-populate migrations table with all consolidated migrations.
INSERT INTO migrations (migration_name, migration_number) VALUES ('001-base.sql', 1);
INSERT INTO migrations (migration_name, migration_number) VALUES ('029-drop-billing-accounts.sql', 29);
INSERT INTO migrations (migration_name, migration_number) VALUES ('030-box-sharing.sql', 30);
INSERT INTO migrations (migration_name, migration_number) VALUES ('031-box-ip-shards.sql', 31);
INSERT INTO migrations (migration_name, migration_number) VALUES ('032-add-host-cert-sig.sql', 32);
INSERT INTO migrations (migration_name, migration_number) VALUES ('033-flatten-allocs.sql', 33);
INSERT INTO migrations (migration_name, migration_number) VALUES ('034-proxy-bearer-tokens.sql', 34);
INSERT INTO migrations (migration_name, migration_number) VALUES ('035-drop-image-metadata.sql', 35);
INSERT INTO migrations (migration_name, migration_number) VALUES ('036-passkeys.sql', 36);
INSERT INTO migrations (migration_name, migration_number) VALUES ('037-support-access.sql', 37);
INSERT INTO migrations (migration_name, migration_number) VALUES ('038-created-for-login-with-exe.sql', 38);
INSERT INTO migrations (migration_name, migration_number) VALUES ('039-new-vm-creation-disabled.sql', 39);
INSERT INTO migrations (migration_name, migration_number) VALUES ('040-email-address-quality.sql', 40);
INSERT INTO migrations (migration_name, migration_number) VALUES ('041-ip-shards.sql', 41);
INSERT INTO migrations (migration_name, migration_number) VALUES ('042-accounts.sql', 42);
INSERT INTO migrations (migration_name, migration_number) VALUES ('043-account-billing-status.sql', 43);
INSERT INTO migrations (migration_name, migration_number) VALUES ('044-hll-sketches.sql', 44);
INSERT INTO migrations (migration_name, migration_number) VALUES ('045-email-bounces.sql', 45);
INSERT INTO migrations (migration_name, migration_number) VALUES ('046-user-llm-credit.sql', 46);
INSERT INTO migrations (migration_name, migration_number) VALUES ('047-signup-rejections.sql', 47);
INSERT INTO migrations (migration_name, migration_number) VALUES ('048-email-quality-bypass.sql', 48);
INSERT INTO migrations (migration_name, migration_number) VALUES ('049-signup-rejection-ipqs-response.sql', 49);
INSERT INTO migrations (migration_name, migration_number) VALUES ('050-shell-history.sql', 50);
INSERT INTO migrations (migration_name, migration_number) VALUES ('051-discord-id.sql', 51);
INSERT INTO migrations (migration_name, migration_number) VALUES ('052-discord-username.sql', 52);
INSERT INTO migrations (migration_name, migration_number) VALUES ('053-invite-codes.sql', 53);
INSERT INTO migrations (migration_name, migration_number) VALUES ('054-email-verifications-invite.sql', 54);
INSERT INTO migrations (migration_name, migration_number) VALUES ('055-pending-registrations.sql', 55);
INSERT INTO migrations (migration_name, migration_number) VALUES ('056-invite-allocated-at.sql', 56);
INSERT INTO migrations (migration_name, migration_number) VALUES ('057-ssh-key-comment.sql', 57);
INSERT INTO migrations (migration_name, migration_number) VALUES ('058-drop-proxy-bearer-tokens.sql', 58);
INSERT INTO migrations (migration_name, migration_number) VALUES ('059-llm-credit-nullable-settings.sql', 59);
INSERT INTO migrations (migration_name, migration_number) VALUES ('060-test-code-migration.sql', 60);
INSERT INTO migrations (migration_name, migration_number) VALUES ('061-ssh-key-fingerprint.sql', 61);
INSERT INTO migrations (migration_name, migration_number) VALUES ('062-backfill-ssh-fingerprints.sql', 62);
INSERT INTO migrations (migration_name, migration_number) VALUES ('063-next-ssh-key-number.sql', 63);
INSERT INTO migrations (migration_name, migration_number) VALUES ('064-backfill-ssh-key-comments.sql', 64);
INSERT INTO migrations (migration_name, migration_number) VALUES ('065-ssh-key-comment-not-null.sql', 65);
INSERT INTO migrations (migration_name, migration_number) VALUES ('066-billing-events.sql', 66);
INSERT INTO migrations (migration_name, migration_number) VALUES ('067-unique-billing-events.sql', 67);
INSERT INTO migrations (migration_name, migration_number) VALUES ('068-drop-billing-status-column.sql', 68);
INSERT INTO migrations (migration_name, migration_number) VALUES ('069-user-defaults.sql', 69);
INSERT INTO migrations (migration_name, migration_number) VALUES ('070-aws-latitude-ip-shards.sql', 70);
INSERT INTO migrations (migration_name, migration_number) VALUES ('071-box-email-credit.sql', 71);
INSERT INTO migrations (migration_name, migration_number) VALUES ('072-add-region-columns.sql', 72);
INSERT INTO migrations (migration_name, migration_number) VALUES ('073-box-email-receive.sql', 73);
INSERT INTO migrations (migration_name, migration_number) VALUES ('074-box-email-maildir-path.sql', 74);
INSERT INTO migrations (migration_name, migration_number) VALUES ('075-email-verifications-is-new-user.sql', 75);
INSERT INTO migrations (migration_name, migration_number) VALUES ('076-email-verifications-redirect.sql', 76);
INSERT INTO migrations (migration_name, migration_number) VALUES ('077-backfill-canonical-column.sql', 77);
INSERT INTO migrations (migration_name, migration_number) VALUES ('078-base.sql', 78);
INSERT INTO migrations (migration_name, migration_number) VALUES ('079-is-locked-out.sql', 79);
INSERT INTO migrations (migration_name, migration_number) VALUES ('080-user-limits.sql', 80);
INSERT INTO migrations (migration_name, migration_number) VALUES ('081-account-credits.sql', 81);
INSERT INTO migrations (migration_name, migration_number) VALUES ('082-checkout-params.sql', 82);
INSERT INTO migrations (migration_name, migration_number) VALUES ('083-cleanup-orphaned-ip-shards.sql', 83);
INSERT INTO migrations (migration_name, migration_number) VALUES ('084-teams.sql', 84);
INSERT INTO migrations (migration_name, migration_number) VALUES ('085-account-credit-hourly-upsert.sql', 85);
INSERT INTO migrations (migration_name, migration_number) VALUES ('086-allocated-cpus.sql', 86);
INSERT INTO migrations (migration_name, migration_number) VALUES ('087-rename-account-credit-ledger-to-billing-credits.sql', 87);
INSERT INTO migrations (migration_name, migration_number) VALUES ('088-bump-llm-credit-to-100.sql', 88);
INSERT INTO migrations (migration_name, migration_number) VALUES ('089-llm-credit-upgrade-bonus-once.sql', 89);
INSERT INTO migrations (migration_name, migration_number) VALUES ('090-pending-team-invites.sql', 90);
INSERT INTO migrations (migration_name, migration_number) VALUES ('091-expand-ip-shard-range.sql', 91);
INSERT INTO migrations (migration_name, migration_number) VALUES ('092-newsletter-subscribed.sql', 92);
INSERT INTO migrations (migration_name, migration_number) VALUES ('093-vm-templates.sql', 93);
INSERT INTO migrations (migration_name, migration_number) VALUES ('094-vm-template-shortname.sql', 94);
INSERT INTO migrations (migration_name, migration_number) VALUES ('095-vm-template-shortname-unique.sql', 95);
INSERT INTO migrations (migration_name, migration_number) VALUES ('096-vm-image.sql', 96);
INSERT INTO migrations (migration_name, migration_number) VALUES ('097-vm-template-image.sql', 97);
INSERT INTO migrations (migration_name, migration_number) VALUES ('098-user-global-load-balancer.sql', 98);
INSERT INTO migrations (migration_name, migration_number) VALUES ('099-auth-provider.sql', 99);
INSERT INTO migrations (migration_name, migration_number) VALUES ('100-redirects.sql', 100);
INSERT INTO migrations (migration_name, migration_number) VALUES ('101-team-sso-providers.sql', 101);
INSERT INTO migrations (migration_name, migration_number) VALUES ('102-invite-batch.sql', 102);
INSERT INTO migrations (migration_name, migration_number) VALUES ('103-app-tokens.sql', 103);
INSERT INTO migrations (migration_name, migration_number) VALUES ('104-verification-code-attempts.sql', 104);
INSERT INTO migrations (migration_name, migration_number) VALUES ('105-team-roles-split.sql', 105);
INSERT INTO migrations (migration_name, migration_number) VALUES ('106-vm-template-deploy-count.sql', 106);
INSERT INTO migrations (migration_name, migration_number) VALUES ('107-integrations.sql', 107);
INSERT INTO migrations (migration_name, migration_number) VALUES ('108-migrations-by-name.sql', 108);
INSERT INTO migrations (migration_name, migration_number) VALUES ('109-exe1-tokens.sql', 109);
INSERT INTO migrations (migration_name, migration_number) VALUES ('110-box-tags.sql', 110);
INSERT INTO migrations (migration_name, migration_number) VALUES ('110-github-accounts.sql', 110);
INSERT INTO migrations (migration_name, migration_number) VALUES ('110-team-id-namespace.sql', 110);
INSERT INTO migrations (migration_name, migration_number) VALUES ('111-github-accounts-multi-install.sql', 111);
INSERT INTO migrations (migration_name, migration_number) VALUES ('111-integration-attachments-column.sql', 111);
INSERT INTO migrations (migration_name, migration_number) VALUES ('111-team-role-sudoer-to-admin.sql', 111);
INSERT INTO migrations (migration_name, migration_number) VALUES ('112-signup-ip-checks.sql', 112);
INSERT INTO migrations (migration_name, migration_number) VALUES ('113-signup-ip-checks-error.sql', 113);
INSERT INTO migrations (migration_name, migration_number) VALUES ('114-github-accounts-target-unique.sql', 114);
INSERT INTO migrations (migration_name, migration_number) VALUES ('114-github-token-renewal.sql', 114);
INSERT INTO migrations (migration_name, migration_number) VALUES ('115-box-lock-reason.sql', 115);
INSERT INTO migrations (migration_name, migration_number) VALUES ('115-released-box-names.sql', 115);
INSERT INTO migrations (migration_name, migration_number) VALUES ('116-netactuate-ip-shards.sql', 116);
INSERT INTO migrations (migration_name, migration_number) VALUES ('117-billing-credits-gift.sql', 117);
INSERT INTO migrations (migration_name, migration_number) VALUES ('118-expand-ip-shard-range-1016.sql', 118);
INSERT INTO migrations (migration_name, migration_number) VALUES ('118-github-integration-flag.sql', 118);
INSERT INTO migrations (migration_name, migration_number) VALUES ('119-account-parent-status.sql', 119);
INSERT INTO migrations (migration_name, migration_number) VALUES ('119-push-tokens.sql', 119);
INSERT INTO migrations (migration_name, migration_number) VALUES ('120-account-plans.sql', 120);
INSERT INTO migrations (migration_name, migration_number) VALUES ('121-push-token-environment.sql', 121);
INSERT INTO migrations (migration_name, migration_number) VALUES ('122-accounts-unique-created-by.sql', 122);
INSERT INTO migrations (migration_name, migration_number) VALUES ('122-fix-github-timestamps.sql', 122);
INSERT INTO migrations (migration_name, migration_number) VALUES ('123-github-normalize-tokens.sql', 123);
INSERT INTO migrations (migration_name, migration_number) VALUES ('123-pending-registrations-account-id.sql', 123);
INSERT INTO migrations (migration_name, migration_number) VALUES ('124-idx-boxes-ctrhost.sql', 124);
INSERT INTO migrations (migration_name, migration_number) VALUES ('125-idx-auth-cookies-domain.sql', 125);
INSERT INTO migrations (migration_name, migration_number) VALUES ('126-idx-boxes-created-by-user-id.sql', 126);
INSERT INTO migrations (migration_name, migration_number) VALUES ('127-new-setup-script.sql', 127);
INSERT INTO migrations (migration_name, migration_number) VALUES ('128-billing-events-stripe-event-id.sql', 128);
INSERT INTO migrations (migration_name, migration_number) VALUES ('128-drop-legacy-billing-columns.sql', 128);
INSERT INTO migrations (migration_name, migration_number) VALUES ('129-dedup-billing-events.sql', 129);
INSERT INTO migrations (migration_name, migration_number) VALUES ('129-stripe-webhook-events.sql', 129);
INSERT INTO migrations (migration_name, migration_number) VALUES ('130-teams-fix-timestamp-defaults.sql', 130);
INSERT INTO migrations (migration_name, migration_number) VALUES ('131-normalize-timestamps.sql', 131);
INSERT INTO migrations (migration_name, migration_number) VALUES ('132-drop-legacy-ip-shard-tables.sql', 132);

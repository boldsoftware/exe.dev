-- Base schema snapshot created by consolidate-migrations.
-- Consolidates migrations 001 through 077.
-- This migration is skipped on existing databases (only runs on fresh databases).
-- All schema changes must be done via new migrations.

CREATE TABLE ssh_host_key ( id INTEGER PRIMARY KEY CHECK (id = 1), private_key TEXT NOT NULL, public_key TEXT NOT NULL, fingerprint TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, cert_sig TEXT);
CREATE TABLE users (
    user_id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
, root_support INTEGER NOT NULL DEFAULT 0, created_for_login_with_exe INTEGER NOT NULL DEFAULT 0, new_vm_creation_disabled INTEGER NOT NULL DEFAULT 0, discord_id TEXT, discord_username TEXT, billing_exemption TEXT CHECK (billing_exemption IN ('trial', 'free') OR billing_exemption IS NULL), billing_trial_ends_at DATETIME, signed_up_with_invite_id INTEGER REFERENCES invite_codes(id), next_ssh_key_number INTEGER NOT NULL DEFAULT 1, region TEXT NOT NULL DEFAULT 'pdx', canonical_email TEXT);
CREATE TABLE email_verifications (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP, verification_code TEXT, invite_code_id INTEGER REFERENCES invite_codes(id), is_new_user INTEGER NOT NULL DEFAULT 0, redirect_url TEXT, return_host TEXT,
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
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
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
CREATE TABLE box_ip_shard (
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
    creation_log TEXT, support_access_allowed INTEGER NOT NULL DEFAULT 0, region TEXT NOT NULL DEFAULT 'pdx', email_receive_enabled INTEGER NOT NULL DEFAULT 0, email_maildir_path TEXT NOT NULL DEFAULT '',
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
CREATE TABLE ip_shards (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
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
    used_at DATETIME, allocated_at TIMESTAMP,
    FOREIGN KEY (assigned_to_user_id) REFERENCES users(user_id) ON DELETE SET NULL,
    FOREIGN KEY (used_by_user_id) REFERENCES users(user_id) ON DELETE SET NULL
);
CREATE INDEX idx_invite_codes_code ON invite_codes(code);
CREATE INDEX idx_invite_codes_assigned_to ON invite_codes(assigned_to_user_id) WHERE assigned_to_user_id IS NOT NULL;
CREATE INDEX idx_invite_codes_unused ON invite_codes(id) WHERE used_by_user_id IS NULL;
CREATE INDEX idx_users_billing_exemption ON users(billing_exemption) WHERE billing_exemption IS NOT NULL;
CREATE INDEX idx_users_trial_ends ON users(billing_trial_ends_at) WHERE billing_trial_ends_at IS NOT NULL;
CREATE TABLE pending_registrations (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    invite_code_id INTEGER REFERENCES invite_codes(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);
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
);
CREATE TABLE IF NOT EXISTS "ssh_keys" (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    public_key TEXT UNIQUE NOT NULL,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
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
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (account_id) REFERENCES accounts(id)
);
CREATE INDEX idx_billing_events_account_event_at ON billing_events(account_id, event_at DESC);
CREATE UNIQUE INDEX idx_billing_events_unique ON billing_events(account_id, event_type, event_at);
CREATE TABLE user_defaults (
    user_id TEXT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    new_vm_email INTEGER, -- NULL = not set (use default), 0 = false, 1 = true
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE aws_ip_shards (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE latitude_ip_shards (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE box_email_credit (
    box_id INTEGER PRIMARY KEY REFERENCES boxes(id),
    available_credit REAL NOT NULL DEFAULT 50.0,
    last_refresh_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    total_sent INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_boxes_region ON boxes(region);
CREATE INDEX idx_users_canonical_email ON users(canonical_email);

-- Pre-populate migrations table with all consolidated migrations.
INSERT INTO migrations (migration_number, migration_name) VALUES (1, '001-base.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (29, '029-drop-billing-accounts.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (30, '030-box-sharing.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (31, '031-box-ip-shards.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (32, '032-add-host-cert-sig.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (33, '033-flatten-allocs.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (34, '034-proxy-bearer-tokens.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (35, '035-drop-image-metadata.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (36, '036-passkeys.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (37, '037-support-access.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (38, '038-created-for-login-with-exe.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (39, '039-new-vm-creation-disabled.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (40, '040-email-address-quality.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (41, '041-ip-shards.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (42, '042-accounts.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (43, '043-account-billing-status.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (44, '044-hll-sketches.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (45, '045-email-bounces.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (46, '046-user-llm-credit.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (47, '047-signup-rejections.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (48, '048-email-quality-bypass.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (49, '049-signup-rejection-ipqs-response.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (50, '050-shell-history.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (51, '051-discord-id.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (52, '052-discord-username.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (53, '053-invite-codes.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (54, '054-email-verifications-invite.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (55, '055-pending-registrations.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (56, '056-invite-allocated-at.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (57, '057-ssh-key-comment.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (58, '058-drop-proxy-bearer-tokens.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (59, '059-llm-credit-nullable-settings.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (60, '060-test-code-migration.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (61, '061-ssh-key-fingerprint.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (62, '062-backfill-ssh-fingerprints.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (63, '063-next-ssh-key-number.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (64, '064-backfill-ssh-key-comments.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (65, '065-ssh-key-comment-not-null.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (66, '066-billing-events.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (67, '067-unique-billing-events.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (68, '068-drop-billing-status-column.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (69, '069-user-defaults.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (70, '070-aws-latitude-ip-shards.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (71, '071-box-email-credit.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (72, '072-add-region-columns.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (73, '073-box-email-receive.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (74, '074-box-email-maildir-path.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (75, '075-email-verifications-is-new-user.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (76, '076-email-verifications-redirect.sql');
INSERT INTO migrations (migration_number, migration_name) VALUES (77, '077-backfill-canonical-column.sql');

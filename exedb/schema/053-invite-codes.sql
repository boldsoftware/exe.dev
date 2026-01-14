-- Invite code system for controlled signups
--
-- Two kinds of invites:
-- 1. User invites: assigned to an existing user who can share them
-- 2. System invites: not assigned to anyone, created via /debug/invite
--
-- When used, the new user gets one of two plan types:
-- - 'trial': 1 month free before Stripe flow is required
-- - 'free': free forever (friends-of-exe)

-- Pool of pre-generated invite codes not yet assigned to anyone
-- These are drawn from when creating new invite codes
CREATE TABLE invite_code_pool (
    code TEXT PRIMARY KEY,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Invite codes that have been created (drawn from the pool)
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
    used_at DATETIME,
    FOREIGN KEY (assigned_to_user_id) REFERENCES users(user_id) ON DELETE SET NULL,
    FOREIGN KEY (used_by_user_id) REFERENCES users(user_id) ON DELETE SET NULL
);

CREATE INDEX idx_invite_codes_code ON invite_codes(code);
CREATE INDEX idx_invite_codes_assigned_to ON invite_codes(assigned_to_user_id) WHERE assigned_to_user_id IS NOT NULL;
CREATE INDEX idx_invite_codes_unused ON invite_codes(id) WHERE used_by_user_id IS NULL;

-- Extend users table with billing exemption fields
-- billing_exemption: null (no exemption), 'trial' (1 month free), 'free' (free forever)
ALTER TABLE users ADD COLUMN billing_exemption TEXT CHECK (billing_exemption IN ('trial', 'free') OR billing_exemption IS NULL);

-- When the trial period ends (only relevant if billing_exemption = 'trial')
ALTER TABLE users ADD COLUMN billing_trial_ends_at DATETIME;

-- Reference to the invite code they used to sign up (for tracking)
ALTER TABLE users ADD COLUMN signed_up_with_invite_id INTEGER REFERENCES invite_codes(id);

CREATE INDEX idx_users_billing_exemption ON users(billing_exemption) WHERE billing_exemption IS NOT NULL;
CREATE INDEX idx_users_trial_ends ON users(billing_trial_ends_at) WHERE billing_trial_ends_at IS NOT NULL;

-- Drip campaign email tracking.
-- Records every email we send (or decide not to send) during trial onboarding.
CREATE TABLE drip_sends (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    campaign TEXT NOT NULL,       -- e.g. 'trial_onboarding'
    step TEXT NOT NULL,            -- e.g. 'day0_welcome', 'day1_nudge'
    status TEXT NOT NULL,          -- 'sent', 'skipped', 'failed'
    skip_reason TEXT,              -- why we chose not to send (NULL if sent)
    email_to TEXT,                 -- recipient address (NULL if skipped)
    email_subject TEXT,            -- subject line (NULL if skipped)
    email_body TEXT,               -- full body (NULL if skipped)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_drip_sends_user_campaign ON drip_sends(user_id, campaign, step);
CREATE INDEX idx_drip_sends_created ON drip_sends(created_at);
CREATE INDEX idx_drip_sends_status ON drip_sends(status, created_at);

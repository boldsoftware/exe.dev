-- Pending registrations track new users through the Stripe checkout flow
-- before their user record is created. Created when a new user enters their
-- email at /auth, deleted after successful Stripe checkout.
CREATE TABLE pending_registrations (
    token TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    invite_code_id INTEGER REFERENCES invite_codes(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);

CREATE INDEX idx_pending_registrations_email ON pending_registrations(email);
CREATE INDEX idx_pending_registrations_expires ON pending_registrations(expires_at);

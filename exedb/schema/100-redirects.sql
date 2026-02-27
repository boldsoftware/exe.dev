-- Short URL redirects for transient, secret, long URLs.
-- Key is crand.Text, value is redirect target URL.
CREATE TABLE redirects (
    key TEXT PRIMARY KEY,
    target TEXT NOT NULL,
    expires_at DATETIME NOT NULL
);
CREATE INDEX idx_redirects_expires ON redirects(expires_at);

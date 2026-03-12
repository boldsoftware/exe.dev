CREATE TABLE signup_ip_checks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    ip TEXT NOT NULL,
    source TEXT NOT NULL,
    ipqs_response_json TEXT,
    flagged INTEGER NOT NULL DEFAULT 0,
    checked_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_signup_ip_checks_email ON signup_ip_checks(email);
CREATE INDEX idx_signup_ip_checks_ip ON signup_ip_checks(ip);
CREATE INDEX idx_signup_ip_checks_checked_at ON signup_ip_checks(checked_at);

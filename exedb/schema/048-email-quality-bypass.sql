-- Allowlist email addresses to bypass quality checks
CREATE TABLE email_quality_bypass (
    email TEXT PRIMARY KEY NOT NULL,
    reason TEXT NOT NULL,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    added_by TEXT NOT NULL
);

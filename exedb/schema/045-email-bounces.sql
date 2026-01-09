-- Track email addresses that have hard bounced or been marked inactive
CREATE TABLE email_bounces (
    email TEXT PRIMARY KEY NOT NULL,
    reason TEXT NOT NULL,
    bounced_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

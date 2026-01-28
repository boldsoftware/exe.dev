-- Token bucket credit system for VM email sending
-- Max 50 emails (burst), refills at 10/day (~0.417/hour)
CREATE TABLE IF NOT EXISTS box_email_credit (
    box_id INTEGER PRIMARY KEY REFERENCES boxes(id),
    available_credit REAL NOT NULL DEFAULT 50.0,
    last_refresh_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    total_sent INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

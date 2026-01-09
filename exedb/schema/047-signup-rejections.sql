-- Track rejected signup attempts with reasons
CREATE TABLE signup_rejections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    ip TEXT NOT NULL,
    reason TEXT NOT NULL,
    source TEXT NOT NULL,
    rejected_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_signup_rejections_email ON signup_rejections(email);
CREATE INDEX idx_signup_rejections_ip ON signup_rejections(ip);
CREATE INDEX idx_signup_rejections_rejected_at ON signup_rejections(rejected_at);

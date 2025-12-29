-- Store email address quality lookups from IPQS
CREATE TABLE email_address_quality (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    queried_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    response_json TEXT NOT NULL,
    disposable INTEGER GENERATED ALWAYS AS (json_extract(response_json, '$.disposable')) STORED
);

CREATE INDEX idx_email_address_quality_email ON email_address_quality(email);

-- Token bucket credit system for LLM gateway usage
-- Credits are stored as floating point USD values
-- Default: $100 max credit, $100 initial, $10/hour refresh

CREATE TABLE IF NOT EXISTS user_llm_credit (
    user_id TEXT PRIMARY KEY REFERENCES users(user_id),
    available_credit REAL NOT NULL DEFAULT 100.0,
    max_credit REAL NOT NULL DEFAULT 100.0,
    refresh_per_hour REAL NOT NULL DEFAULT 10.0,
    total_used REAL NOT NULL DEFAULT 0.0,
    last_refresh_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

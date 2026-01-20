-- Make max_credit and refresh_per_hour nullable to support policy-based defaults
-- When NULL, the code will use default values (currently 100 and 10)

PRAGMA foreign_keys = OFF;

ALTER TABLE user_llm_credit RENAME TO user_llm_credit_old;

CREATE TABLE IF NOT EXISTS user_llm_credit (
    user_id TEXT PRIMARY KEY REFERENCES users(user_id),
    available_credit REAL NOT NULL DEFAULT 100.0,
    max_credit REAL,  -- NULL means use default (currently 100.0)
    refresh_per_hour REAL,  -- NULL means use default (currently 10.0)
    total_used REAL NOT NULL DEFAULT 0.0,
    last_refresh_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Copy data, setting max_credit and refresh_per_hour to NULL
-- (they were all 100/10 anyway, which is the default we'll use)
INSERT INTO user_llm_credit (user_id, available_credit, max_credit, refresh_per_hour, total_used, last_refresh_at, created_at, updated_at)
SELECT user_id, available_credit, NULL, NULL, total_used, last_refresh_at, created_at, updated_at
FROM user_llm_credit_old;

DROP TABLE user_llm_credit_old;

PRAGMA foreign_keys = ON;

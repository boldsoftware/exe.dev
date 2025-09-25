-- Welcome demo DB schema (sqlite)
CREATE TABLE IF NOT EXISTS visitors (
    id TEXT PRIMARY KEY,
    email TEXT,
    view_count INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL,
    last_seen TIMESTAMP NOT NULL
);


CREATE TABLE other_stripe_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stripe_event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    api_version TEXT,
    stripe_created_at INTEGER NOT NULL,
    received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    source TEXT NOT NULL,
    payload TEXT NOT NULL
);
CREATE INDEX idx_other_stripe_events_type ON other_stripe_events(event_type, stripe_created_at);
CREATE INDEX idx_other_stripe_events_received ON other_stripe_events(received_at);

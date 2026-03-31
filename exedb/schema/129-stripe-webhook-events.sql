CREATE TABLE stripe_webhook_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stripe_event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_stripe_webhook_events_type ON stripe_webhook_events(event_type);
CREATE INDEX idx_stripe_webhook_events_created ON stripe_webhook_events(created_at);

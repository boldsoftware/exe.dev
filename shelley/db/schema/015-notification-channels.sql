-- Notification channels table
-- Stores user-configured notification channels (Discord webhooks, etc.)

CREATE TABLE notification_channels (
    channel_id TEXT PRIMARY KEY,
    channel_type TEXT NOT NULL,
    display_name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    config TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

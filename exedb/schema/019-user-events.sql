-- Add user_events table for tracking user interactions and accomplishments
-- This table tracks events as user + event => count pairs where:
-- - user_id: the user who performed the action
-- - event: the name/type of event (e.g., "created_box", "shown_tip_new_command", etc.)
-- - count: how many times this event has occurred (starts at 0, incremented as needed)

CREATE TABLE IF NOT EXISTS user_events (
    user_id TEXT NOT NULL,
    event TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    first_occurred_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_occurred_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, event),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Record this migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (015, '015_user_events');
-- Tracks LMTP inbound delivery failures per box and error class so we can
-- notify the box owner at most once per (box, error class) every 3 days
-- without spamming them on every retry.
CREATE TABLE lmtp_delivery_failures (
    box_id INTEGER NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
    error_class TEXT NOT NULL,            -- e.g. 'disk_full', 'maildir_missing', 'other'
    failure_count INTEGER NOT NULL DEFAULT 1,
    last_failure_at DATETIME NOT NULL,
    last_error TEXT NOT NULL,
    last_notified_at DATETIME,
    PRIMARY KEY (box_id, error_class)
);

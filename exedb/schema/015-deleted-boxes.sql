-- Track deleted boxes to preserve their disk data
-- This allows us to implement PurgeDeletedDisks in the future
CREATE TABLE IF NOT EXISTS deleted_boxes (
    id INTEGER PRIMARY KEY,  -- Same as the original box id
    alloc_id TEXT NOT NULL,
    deleted_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_deleted_boxes_alloc_id ON deleted_boxes(alloc_id);
CREATE INDEX IF NOT EXISTS idx_deleted_boxes_deleted_at ON deleted_boxes(deleted_at);
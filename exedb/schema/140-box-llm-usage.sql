CREATE TABLE box_llm_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    hour_bucket DATETIME NOT NULL,
    cost_microcents INTEGER NOT NULL DEFAULT 0,
    request_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- user_id is intentionally excluded from the uniqueness key. If a box changes
-- owners mid-hour, usage for that hour remains attributed to the original owner.
CREATE UNIQUE INDEX idx_box_llm_usage_box_model_hour
ON box_llm_usage(box_id, provider, model, hour_bucket);
CREATE INDEX idx_box_llm_usage_user_hour
ON box_llm_usage(user_id, hour_bucket);

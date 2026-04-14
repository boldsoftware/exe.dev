CREATE TABLE user_region_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL,
    rollback_of_migration_id INTEGER REFERENCES user_region_migrations(id),
    user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('dry_run', 'apply', 'rollback')),
    status TEXT NOT NULL CHECK (status IN (
        'dry_run_planned',
        'apply_pending',
        'apply_succeeded',
        'apply_failed',
        'rollback_pending',
        'rollback_succeeded',
        'rollback_failed'
    )),
    old_region TEXT NOT NULL,
    target_region TEXT NOT NULL,
    decision_source TEXT NOT NULL DEFAULT '',
    decision_reason TEXT NOT NULL DEFAULT '',
    signup_ip_check_id INTEGER REFERENCES signup_ip_checks(id),
    error TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
CREATE INDEX idx_user_region_migrations_batch_id ON user_region_migrations(batch_id);
CREATE INDEX idx_user_region_migrations_user_id ON user_region_migrations(user_id);
CREATE INDEX idx_user_region_migrations_rollback_of ON user_region_migrations(rollback_of_migration_id);

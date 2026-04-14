-- name: InsertUserRegionMigration :one
INSERT INTO user_region_migrations (
    batch_id,
    rollback_of_migration_id,
    user_id,
    email,
    mode,
    status,
    old_region,
    target_region,
    decision_source,
    decision_reason,
    signup_ip_check_id,
    error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateUserRegionMigrationResult :exec
UPDATE user_region_migrations
SET status = ?, error = ?, completed_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: ListUserRegionMigrationsByBatch :many
SELECT *
FROM user_region_migrations
WHERE batch_id = ?
ORDER BY id ASC;

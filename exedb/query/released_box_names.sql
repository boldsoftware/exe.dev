-- name: InsertReleasedBoxName :exec
INSERT OR REPLACE INTO released_box_names (name, box_id, user_id, released_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP);

-- name: DeleteReleasedBoxName :exec
DELETE FROM released_box_names WHERE name = ?;

-- name: GetReleasedBoxName :one
SELECT * FROM released_box_names WHERE name = ? AND released_at > datetime('now', '-24 hours');

-- name: CleanupExpiredReleasedBoxNames :exec
DELETE FROM released_box_names WHERE released_at <= datetime('now', '-24 hours');

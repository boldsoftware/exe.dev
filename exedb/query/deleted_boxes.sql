-- name: InsertDeletedBox :exec
INSERT OR IGNORE INTO deleted_boxes (id, user_id) VALUES (?, ?);

-- name: InsertDeletedBox :exec
INSERT INTO deleted_boxes (id, user_id) VALUES (?, ?);

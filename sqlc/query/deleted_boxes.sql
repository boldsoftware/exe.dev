-- name: InsertDeletedBox :exec
INSERT INTO deleted_boxes (id, alloc_id) VALUES (?, ?);

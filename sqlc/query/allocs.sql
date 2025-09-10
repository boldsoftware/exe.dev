-- name: AllocExistsForUser :one
SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?);

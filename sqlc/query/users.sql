-- name: GetUserIDByEmail :one
SELECT user_id FROM users WHERE email = ?;

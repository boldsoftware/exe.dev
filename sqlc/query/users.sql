-- name: GetUserIDByEmail :one
SELECT user_id FROM users WHERE email = ?;

-- name: GetFirstUserID :one
SELECT user_id FROM users LIMIT 1;

-- name: InsertUser :exec
INSERT INTO users (user_id, email) VALUES (?, ?);

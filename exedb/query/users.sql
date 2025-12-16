-- name: GetUserIDByEmail :one
SELECT user_id FROM users WHERE email = ?;

-- name: InsertUser :exec
INSERT INTO users (user_id, email, created_for_login_with_exe) VALUES (?, ?, ?);

-- name: GetUserWithDetails :one
SELECT *
FROM users
WHERE user_id = ?;

-- name: GetUserByEmail :one
SELECT *
FROM users
WHERE email = ?;

-- name: GetEmailByUserID :one
SELECT email FROM users WHERE user_id = ?;

-- name: ListAllUsers :many
SELECT * FROM users ORDER BY created_at DESC;

-- name: SetUserRootSupport :exec
UPDATE users SET root_support = ? WHERE user_id = ?;

-- name: GetUserRootSupport :one
SELECT root_support FROM users WHERE user_id = ?;

-- name: CountLoginUsers :one
SELECT COUNT(*) FROM users WHERE created_for_login_with_exe = 1;

-- name: CountDevUsers :one
SELECT COUNT(*) FROM users WHERE created_for_login_with_exe = 0;

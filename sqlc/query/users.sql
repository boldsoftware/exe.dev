-- name: GetUserIDByEmail :one
SELECT user_id FROM users WHERE email = ?;

-- name: InsertUser :exec
INSERT INTO users (user_id, email, default_billing_account_id) VALUES (?, ?, ?);

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

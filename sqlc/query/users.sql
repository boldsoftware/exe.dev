-- name: GetUserIDByEmail :one
SELECT user_id FROM users WHERE email = ?;

-- name: GetFirstUserID :one
SELECT user_id FROM users LIMIT 1;

-- name: InsertUser :exec
INSERT INTO users (user_id, email, default_billing_account_id) VALUES (?, ?, ?);

-- name: GetUserWithDetails :one
SELECT user_id, email, created_at, default_billing_account_id
FROM users
WHERE user_id = ?;

-- name: GetUserByEmail :one
SELECT user_id, email, created_at, default_billing_account_id
FROM users
WHERE email = ?;

-- name: GetEmailByUserID :one
SELECT email FROM users WHERE user_id = ?;

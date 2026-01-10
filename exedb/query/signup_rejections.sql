-- name: InsertSignupRejection :exec
INSERT INTO signup_rejections (email, ip, reason, source) VALUES (?, ?, ?, ?);

-- name: GetSignupRejectionsByEmail :many
SELECT * FROM signup_rejections WHERE email = ? ORDER BY rejected_at DESC;

-- name: GetSignupRejectionsByIP :many
SELECT * FROM signup_rejections WHERE ip = ? ORDER BY rejected_at DESC;

-- name: GetRecentSignupRejections :many
SELECT * FROM signup_rejections ORDER BY rejected_at DESC LIMIT ?;

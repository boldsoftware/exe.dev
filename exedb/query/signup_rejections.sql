-- name: InsertSignupRejection :exec
INSERT INTO signup_rejections (email, ip, reason, source, ipqs_response_json) VALUES (?, ?, ?, ?, ?);

-- name: GetRecentSignupRejections :many
SELECT * FROM signup_rejections ORDER BY rejected_at DESC LIMIT ?;

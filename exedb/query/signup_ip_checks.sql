-- name: InsertSignupIPCheck :exec
INSERT INTO signup_ip_checks (email, ip, source, ipqs_response_json, flagged) VALUES (?, ?, ?, ?, ?);

-- name: GetSignupIPChecksByEmail :many
SELECT * FROM signup_ip_checks WHERE email = ? ORDER BY checked_at DESC;

-- name: GetSignupIPChecksByIP :many
SELECT * FROM signup_ip_checks WHERE ip = ? ORDER BY checked_at DESC;

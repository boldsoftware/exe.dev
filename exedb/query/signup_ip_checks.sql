-- name: InsertSignupIPCheck :exec
INSERT INTO signup_ip_checks (email, ip, source, ipqs_response_json, error, flagged) VALUES (?, ?, ?, ?, ?, ?);


-- name: GetLatestSignupIPCheckByEmail :one
SELECT *
FROM signup_ip_checks
WHERE lower(email) = lower(?)
ORDER BY checked_at DESC, id DESC
LIMIT 1;

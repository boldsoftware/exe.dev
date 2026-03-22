-- name: InsertSignupIPCheck :exec
INSERT INTO signup_ip_checks (email, ip, source, ipqs_response_json, error, flagged) VALUES (?, ?, ?, ?, ?, ?);

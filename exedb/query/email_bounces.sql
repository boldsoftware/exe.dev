-- name: InsertEmailBounce :exec
INSERT OR REPLACE INTO email_bounces (email, reason) VALUES (?, ?);

-- name: GetEmailBounce :one
SELECT * FROM email_bounces WHERE email = ?;

-- name: IsEmailBounced :one
SELECT EXISTS(SELECT 1 FROM email_bounces WHERE email = ?) AS bounced;

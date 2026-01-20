-- name: InsertEmailBounce :exec
INSERT OR REPLACE INTO email_bounces (email, reason) VALUES (?, ?);

-- name: GetEmailBounce :one
SELECT * FROM email_bounces WHERE email = ?;

-- name: IsEmailBounced :one
SELECT EXISTS(SELECT 1 FROM email_bounces WHERE email = ?) AS bounced;

-- name: ListEmailBounces :many
SELECT * FROM email_bounces ORDER BY bounced_at DESC;

-- name: CountEmailBounces :one
SELECT COUNT(*) FROM email_bounces;

-- name: DeleteEmailBounce :exec
DELETE FROM email_bounces WHERE email = ?;

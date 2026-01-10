-- name: InsertEmailQualityBypass :exec
INSERT INTO email_quality_bypass (email, reason, added_by) VALUES (?, ?, ?);

-- name: GetEmailQualityBypass :one
SELECT * FROM email_quality_bypass WHERE email = ?;

-- name: IsEmailQualityBypassed :one
SELECT EXISTS(SELECT 1 FROM email_quality_bypass WHERE email = ?) AS bypassed;

-- name: DeleteEmailQualityBypass :exec
DELETE FROM email_quality_bypass WHERE email = ?;

-- name: ListEmailQualityBypass :many
SELECT * FROM email_quality_bypass ORDER BY added_at DESC;

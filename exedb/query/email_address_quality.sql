-- name: InsertEmailAddressQuality :exec
INSERT INTO email_address_quality (email, response_json) VALUES (?, ?);

-- name: GetEmailAddressQualityByEmail :one
SELECT * FROM email_address_quality WHERE email = ? ORDER BY queried_at DESC LIMIT 1;

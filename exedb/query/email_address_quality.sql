-- name: InsertEmailAddressQuality :exec
INSERT INTO email_address_quality (email, response_json) VALUES (?, ?);

-- name: GetBoxEmailCredit :one
SELECT * FROM box_email_credit WHERE box_id = ?;

-- name: CreateBoxEmailCredit :exec
INSERT OR IGNORE INTO box_email_credit (box_id, available_credit, last_refresh_at)
VALUES (?, ?, ?);

-- name: UpdateBoxEmailCredit :exec
UPDATE box_email_credit
SET available_credit = ?, last_refresh_at = ?, total_sent = total_sent + 1
WHERE box_id = ?;

-- name: GetBoxWithOwnerEmail :one
SELECT b.id, b.name, b.created_by_user_id, u.email as owner_email
FROM boxes b
JOIN users u ON u.user_id = b.created_by_user_id
WHERE b.name = ?;

-- name: InsertCheckoutParams :exec
INSERT INTO checkout_params (token, user_id, source, vm_name, vm_prompt) VALUES (?, ?, ?, ?, ?);

-- name: GetCheckoutParams :one
SELECT * FROM checkout_params WHERE token = ? AND user_id = ?;

-- name: ConsumeCheckoutParams :one
DELETE FROM checkout_params WHERE token = ? AND user_id = ? RETURNING *;

-- name: DeleteOldCheckoutParams :exec
DELETE FROM checkout_params WHERE created_at < ?;

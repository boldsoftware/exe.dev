-- name: SetBoxEmailReceiveEnabled :exec
UPDATE boxes SET email_receive_enabled = ? WHERE id = ?;

-- name: GetBoxByNameWithEmailReceiveEnabled :one
SELECT * FROM boxes WHERE name = ? AND email_receive_enabled = 1;

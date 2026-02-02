-- name: SetBoxEmailReceive :exec
-- Sets email receive status and maildir path. Pass empty string when disabling.
UPDATE boxes SET email_receive_enabled = ?, email_maildir_path = ? WHERE id = ?;

-- name: GetBoxByNameWithEmailReceiveEnabled :one
SELECT * FROM boxes WHERE name = ? AND email_receive_enabled = 1;

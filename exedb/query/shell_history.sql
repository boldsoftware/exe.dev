-- name: AddShellHistory :exec
INSERT INTO shell_history (user_id, command)
VALUES (?, ?);

-- name: GetShellHistory :many
SELECT command FROM shell_history
WHERE user_id = ?
ORDER BY id ASC
LIMIT 1024;

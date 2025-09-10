-- name: GetSSHHostKey :one
SELECT private_key, public_key FROM ssh_host_key WHERE id = 1;

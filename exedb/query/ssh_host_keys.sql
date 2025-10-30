-- name: GetSSHHostKey :one
SELECT private_key, public_key, cert_sig FROM ssh_host_key WHERE id = 1;

-- name: GetSSHHostPublicKey :one
SELECT public_key FROM ssh_host_key WHERE id = 1;

-- name: UpsertSSHHostKey :exec
INSERT INTO ssh_host_key (id, private_key, public_key, fingerprint, created_at, updated_at)
VALUES (1, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
  private_key = excluded.private_key,
  public_key = excluded.public_key,
  fingerprint = excluded.fingerprint,
  updated_at = CURRENT_TIMESTAMP;

-- name: GetMobilePendingVMByToken :one
SELECT hostname, prompt, vm_image FROM mobile_pending_vm WHERE token = ?;

-- name: DeleteMobilePendingVMByToken :exec
DELETE FROM mobile_pending_vm WHERE token = ?;

-- name: DeleteMobilePendingVMByUserAndHostname :exec
DELETE FROM mobile_pending_vm WHERE user_id = ? AND hostname = ?;

-- name: UpsertMobilePendingVM :exec
INSERT OR REPLACE INTO mobile_pending_vm (token, user_id, hostname, prompt, vm_image) VALUES (?, ?, ?, ?, ?);

-- name: GetLatestMobilePendingVMByUser :one
SELECT hostname, prompt, vm_image FROM mobile_pending_vm WHERE user_id = ? ORDER BY created_at DESC LIMIT 1;

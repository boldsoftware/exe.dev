-- name: InsertWorkerJob :one
INSERT INTO worker_jobs (event, payload) VALUES (?, ?) RETURNING *;

-- name: ListPendingWorkerJobs :many
SELECT * FROM worker_jobs WHERE status = 'pending' ORDER BY id;

-- name: UpdateWorkerJobStatus :exec
UPDATE worker_jobs SET status = ? WHERE id = ?;

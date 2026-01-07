-- name: GetHLLSketch :one
SELECT key, data FROM hll_sketches WHERE key = ?;

-- name: UpsertHLLSketch :exec
INSERT INTO hll_sketches (key, data, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET data = excluded.data, updated_at = CURRENT_TIMESTAMP;

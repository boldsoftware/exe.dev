-- name: UpsertVisitor :exec
INSERT INTO visitors (id, email, view_count, created_at, last_seen)
VALUES (?, ?, 1, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  view_count = view_count + 1,
  email = COALESCE(excluded.email, visitors.email),
  last_seen = excluded.last_seen;

-- name: GetVisitor :one
SELECT id, email, view_count, created_at, last_seen FROM visitors WHERE id = ?;


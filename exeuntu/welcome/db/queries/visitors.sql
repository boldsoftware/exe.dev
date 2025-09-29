-- name: UpsertVisitor :exec
INSERT INTO
  visitors (id, view_count, created_at, last_seen)
VALUES
  (?, 1, ?, ?) ON CONFLICT (id) DO
UPDATE
SET
  view_count = view_count + 1,
  last_seen = excluded.last_seen;

-- name: VisitorWithID :one
SELECT
  *
FROM
  visitors
WHERE
  id = ?;

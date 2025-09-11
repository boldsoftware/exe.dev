-- name: RecordUserEvent :exec
INSERT INTO user_events (user_id, event, count, first_occurred_at, last_occurred_at)
VALUES (?, ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(user_id, event) DO UPDATE SET
    count = count + 1,
    last_occurred_at = CURRENT_TIMESTAMP;

-- name: GetUserEventCount :one
SELECT COALESCE(count, 0) FROM user_events WHERE user_id = ? AND event = ?;

-- name: GetAllUserEvents :many
SELECT event, count FROM user_events WHERE user_id = ?;

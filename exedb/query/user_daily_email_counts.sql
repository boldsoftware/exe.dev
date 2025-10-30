-- name: GetUserEmailCountForDate :one
SELECT email_count FROM user_daily_email_counts
WHERE user_id = ? AND date = ?;

-- name: IncrementUserEmailCount :exec
INSERT INTO user_daily_email_counts (user_id, date, email_count)
VALUES (?, ?, 1)
ON CONFLICT(user_id, date) DO UPDATE SET
    email_count = email_count + 1;

-- name: GetUserEmailCountsForDateRange :many
SELECT * FROM user_daily_email_counts
WHERE user_id = ? AND date >= ? AND date <= ?
ORDER BY date DESC;

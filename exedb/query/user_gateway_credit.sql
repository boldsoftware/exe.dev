-- name: GetUserLLMCredit :one
SELECT * FROM user_llm_credit WHERE user_id = ?;

-- name: UpsertUserLLMCredit :exec
INSERT INTO user_llm_credit (user_id, available_credit, max_credit, refresh_per_hour, last_refresh_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET
    available_credit = excluded.available_credit,
    max_credit = excluded.max_credit,
    refresh_per_hour = excluded.refresh_per_hour,
    last_refresh_at = excluded.last_refresh_at,
    updated_at = CURRENT_TIMESTAMP;

-- name: UpdateUserLLMAvailableCredit :exec
UPDATE user_llm_credit
SET available_credit = ?, last_refresh_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE user_id = ?;

-- name: DebitUserLLMCredit :exec
UPDATE user_llm_credit
SET available_credit = ?, total_used = total_used + ?, last_refresh_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE user_id = ?;

-- name: UpdateUserLLMCreditSettings :exec
UPDATE user_llm_credit
SET available_credit = ?, max_credit = ?, refresh_per_hour = ?, updated_at = CURRENT_TIMESTAMP
WHERE user_id = ?;

-- name: ListAllUserLLMCredits :many
SELECT * FROM user_llm_credit ORDER BY user_id;

-- name: CreateUserLLMCreditIfNotExists :exec
INSERT OR IGNORE INTO user_llm_credit (user_id) VALUES (?);

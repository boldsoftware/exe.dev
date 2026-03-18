-- name: GetUserLLMCredit :one
SELECT * FROM user_llm_credit WHERE user_id = ?;

-- name: UpsertUserLLMCredit :exec
-- Used for setting explicit overrides. Pass NULL for max_credit/refresh_per_hour to use defaults.
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

-- name: ListAllUserLLMCredits :many
SELECT * FROM user_llm_credit ORDER BY user_id;

-- name: CreateUserLLMCreditWithInitial :exec
INSERT OR IGNORE INTO user_llm_credit (user_id, available_credit, last_refresh_at)
VALUES (?, ?, ?);

-- Deprecated: GrantBillingUpgradeBonusOnce is superseded by billing.GiftCredits
-- with billing.GiftPrefixSignup. Remove once the old credit path is fully migrated.
-- name: GrantBillingUpgradeBonusOnce :exec
INSERT INTO user_llm_credit (user_id, available_credit, last_refresh_at, billing_upgrade_bonus_granted)
VALUES (?, ?, ?, 1)
ON CONFLICT(user_id) DO UPDATE SET
    available_credit = user_llm_credit.available_credit + 100.0,
    billing_upgrade_bonus_granted = 1,
    updated_at = CURRENT_TIMESTAMP
WHERE user_llm_credit.billing_upgrade_bonus_granted = 0;

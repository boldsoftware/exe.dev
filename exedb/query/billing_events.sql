-- name: InsertBillingEvent :execresult
INSERT OR IGNORE INTO billing_events (account_id, event_type, event_at) VALUES (?, ?, ?);

-- name: GetLatestBillingStatus :one
SELECT event_type FROM billing_events WHERE account_id = ? ORDER BY event_at DESC, id DESC LIMIT 1;

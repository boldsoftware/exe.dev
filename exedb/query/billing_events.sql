-- name: InsertBillingEvent :exec
-- event_at should be a string in Time10 format (YYYY-MM-DD HH:MM:SS.nnnnnnnnn-HH:MM)
-- to ensure consistent storage and comparison. Use sqlite.FormatTime(t) to format.
-- stripe_event_id provides idempotent dedup for Stripe-sourced events;
-- NULL for non-Stripe inserts (checkout, debug), which still dedup via
-- the (account_id, event_type, event_at) unique index.
INSERT OR IGNORE INTO billing_events (account_id, event_type, event_at, stripe_event_id) VALUES (?, ?, ?, ?);

-- name: GetLatestBillingStatus :one
SELECT event_type FROM billing_events WHERE account_id = ? ORDER BY parse_timestamp(event_at) DESC, id DESC LIMIT 1;

-- name: ListBillingEventsForAccount :many
SELECT id, account_id, event_type, event_at, created_at
FROM billing_events WHERE account_id = ?
ORDER BY id DESC;

-- name: ListSubscriptionEvents :many
-- ListSubscriptionEvents returns subscription events for an account, ordered by time ascending.
SELECT account_id, event_type, event_at
FROM billing_events
WHERE account_id = ?
ORDER BY event_at ASC;

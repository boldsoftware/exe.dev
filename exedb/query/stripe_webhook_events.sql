-- name: InsertStripeWebhookEvent :exec
INSERT OR IGNORE INTO stripe_webhook_events (stripe_event_id, event_type, payload) VALUES (?, ?, ?);

-- name: ListStripeWebhookEventsByType :many
SELECT id, stripe_event_id, event_type, payload, created_at
FROM stripe_webhook_events
WHERE event_type = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListAllStripeWebhookEvents :many
SELECT id, stripe_event_id, event_type, created_at
FROM stripe_webhook_events
ORDER BY created_at DESC;

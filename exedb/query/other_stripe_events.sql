-- name: InsertOtherStripeEvent :exec
INSERT OR IGNORE INTO other_stripe_events (stripe_event_id, event_type, api_version, stripe_created_at, source, payload)
VALUES (?, ?, ?, ?, ?, ?);

-- name: CountOtherStripeEventsByID :one
SELECT COUNT(*) FROM other_stripe_events WHERE stripe_event_id = ?;

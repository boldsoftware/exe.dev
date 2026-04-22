-- name: RecordLMTPDeliveryFailure :exec
-- Upsert a failure record for a (box, error class) pair.
INSERT INTO lmtp_delivery_failures (box_id, error_class, failure_count, last_failure_at, last_error)
VALUES (?, ?, 1, CURRENT_TIMESTAMP, ?)
ON CONFLICT(box_id, error_class) DO UPDATE SET
    failure_count = failure_count + 1,
    last_failure_at = CURRENT_TIMESTAMP,
    last_error = excluded.last_error;

-- name: HasLMTPDeliveryFailures :one
-- Report whether any failure rows exist for a box. Used on the success path
-- to avoid taking a write transaction to DELETE zero rows, which is the
-- common case.
SELECT EXISTS(SELECT 1 FROM lmtp_delivery_failures WHERE box_id = ?);

-- name: ClearLMTPDeliveryFailures :exec
-- Delete all failure rows for a box. Called on the success path so the
-- notification throttle resets per incident: if a box recovers and then
-- fails again outside the notification interval, the next failure produces
-- a fresh notification rather than being silently suppressed by a stale
-- last_notified_at from a previous incident.
DELETE FROM lmtp_delivery_failures WHERE box_id = ?;

-- name: ClaimLMTPDeliveryFailureNotification :one
-- Atomically claim the notification slot for (box_id, error_class) if the
-- last notification is older than the given interval (seconds), or has never
-- been sent. Returns one row (box_id) on success; sql.ErrNoRows if another
-- caller already claimed it or the interval has not elapsed.
UPDATE lmtp_delivery_failures
SET last_notified_at = CURRENT_TIMESTAMP
WHERE box_id = sqlc.arg(box_id)
  AND error_class = sqlc.arg(error_class)
  AND (last_notified_at IS NULL
       OR unixepoch(CURRENT_TIMESTAMP) - unixepoch(last_notified_at) >= CAST(sqlc.arg(interval_seconds) AS INTEGER))
RETURNING box_id;

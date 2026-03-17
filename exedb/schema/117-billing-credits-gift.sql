-- Add a note column for gift credits (optional human-readable reason).
ALTER TABLE billing_credits ADD COLUMN note TEXT;

-- gift_id provides idempotency for gift inserts, similar to stripe_event_id for purchases.
ALTER TABLE billing_credits ADD COLUMN gift_id TEXT;

-- Unique index on gift_id for idempotency (SQLite cannot ADD COLUMN with UNIQUE inline).
CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_credits_gift_id ON billing_credits(gift_id);

-- Prevent duplicate billing events from being inserted by both webhook and poller
CREATE UNIQUE INDEX idx_billing_events_unique ON billing_events(account_id, event_type, event_at);

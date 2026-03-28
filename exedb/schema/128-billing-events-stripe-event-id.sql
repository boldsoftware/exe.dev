ALTER TABLE billing_events ADD COLUMN stripe_event_id TEXT;
CREATE UNIQUE INDEX idx_billing_events_stripe_event_id ON billing_events(stripe_event_id) WHERE stripe_event_id IS NOT NULL;

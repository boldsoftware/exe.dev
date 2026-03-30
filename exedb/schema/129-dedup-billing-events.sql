-- Delete non-canonical billing_events that duplicate an already-canonical row.
-- These are the same event recorded twice: once in Go time.String() format
-- (before the driver change) and once in YYYY-MM-DD HH:MM:SS format (after).
-- The Stripe event sync on 2026-03-30 re-inserted all historical events with
-- canonical timestamps and stripe_event_ids; the old Go-format rows (NULL
-- stripe_event_id) are now redundant.
--
-- We match by comparing the first 19 characters of the non-canonical
-- event_at (the "YYYY-MM-DD HH:MM:SS" prefix) against the canonical row's
-- event_at, which is exactly 19 characters.
DELETE FROM billing_events WHERE id IN (
  SELECT b1.id FROM billing_events b1
  JOIN billing_events b2 ON b1.account_id = b2.account_id AND b1.event_type = b2.event_type
  WHERE b1.event_at != b2.event_at
    AND b2.event_at GLOB '????-??-?? ??:??:??'
    AND substr(b1.event_at, 1, 19) = b2.event_at
    AND b1.stripe_event_id IS NULL
    AND b1.id != b2.id
);

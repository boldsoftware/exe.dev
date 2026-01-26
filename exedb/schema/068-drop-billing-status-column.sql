-- Drop the billing_status column from accounts table.
-- This column is no longer used - billing status is now computed dynamically
-- from the billing_events table using event sourcing.
ALTER TABLE accounts DROP COLUMN billing_status;

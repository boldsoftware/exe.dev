-- Drop legacy billing columns from users table.
-- These have been replaced by the account_plans table (migration 120).
-- All code now uses account_plans.plan_id instead of users.billing_exemption.

DROP INDEX IF EXISTS idx_users_billing_exemption;
DROP INDEX IF EXISTS idx_users_trial_ends;

ALTER TABLE users DROP COLUMN billing_exemption;
ALTER TABLE users DROP COLUMN billing_trial_ends_at;
